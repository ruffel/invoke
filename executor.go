package invoke

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

const (
	// lineScannerStart and lineScannerMax bound the line buffer used by
	// Lines: a line longer than the max fails the stream rather than
	// growing without limit.
	lineScannerStart = 64 * 1024
	lineScannerMax   = 1024 * 1024

	// maxStderrTail bounds the stderr snippet attached to an ExitError by
	// Output, for diagnostics without unbounded memory.
	maxStderrTail = 8 * 1024
)

// Executor adds invocation policy — retry, sudo, and output capture — on
// top of an [Environment]. The Environment is the mechanism (start a
// process, move a file); the Executor is the policy layer over it.
//
// Only the operations that carry policy are exposed here (Run, Output,
// Lines, Upload, Download). For LookPath, OS, Capabilities, or Close, use
// the Environment directly; if you need no policy at all, you need no
// Executor.
type Executor struct {
	env  Environment
	base execConfig
}

// NewExecutor returns an Executor over env. The options set defaults that
// every call inherits and may override.
func NewExecutor(env Environment, opts ...Option) *Executor {
	cfg := defaultExecConfig()
	for _, opt := range opts {
		opt(&cfg)
	}

	return &Executor{env: env, base: cfg}
}

// Run starts cmd with the given IO, waits for it, and returns the outcome,
// applying the configured retry and sudo policy.
//
// When retries are configured and stdio carries a non-nil Stdin, a
// [WithFreshIO] provider is required: a consumed reader cannot be replayed
// across attempts. Stdout and Stderr writers are reused across attempts as
// given unless WithFreshIO replaces them, so a caller capturing into a
// shared buffer under retry should prefer [Executor.Output].
func (e *Executor) Run(ctx context.Context, cmd Command, stdio IO, opts ...Option) (Result, error) {
	cfg, err := e.configFor(opts)
	if err != nil {
		return Result{}, err
	}

	if cfg.attempts > 1 && cfg.freshIO == nil && stdio.Stdin != nil {
		return Result{}, errors.New(
			"invoke: retrying a command with a non-nil Stdin requires WithFreshIO; a consumed reader cannot be replayed")
	}

	cmd = applySudo(cfg, cmd)

	return e.runWithRetry(ctx, cfg, func(attempt int) (Result, error) {
		attemptIO := stdio
		if cfg.freshIO != nil {
			attemptIO = cfg.freshIO(attempt)
		}

		return e.once(ctx, cmd, attemptIO)
	})
}

// Output runs cmd and returns its captured stdout and stderr. It is
// retry-safe by construction: each attempt writes into fresh buffers, so a
// failed attempt's partial output never accumulates into the result. On an
// [ExitError], a tail of stderr is attached for diagnostics.
func (e *Executor) Output(ctx context.Context, cmd Command, opts ...Option) (Result, []byte, []byte, error) {
	var stdout, stderr bytes.Buffer

	fresh := func(int) IO {
		stdout.Reset()
		stderr.Reset()

		return IO{Stdout: &stdout, Stderr: &stderr}
	}

	// Output owns the streams, so it forces its own fresh-IO provider
	// after the caller's options (retry safety is not negotiable here).
	res, err := e.Run(ctx, cmd, IO{}, append(opts, WithFreshIO(fresh))...)

	var exitErr *ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) == 0 {
		exitErr.Stderr = tail(stderr.Bytes(), maxStderrTail)
	}

	return res, stdout.Bytes(), stderr.Bytes(), err
}

// Lines runs cmd and calls onLine for each line of its standard output.
// Stderr is discarded. It applies the same retry and sudo policy as Run;
// note that a failed-then-retried attempt may have already delivered some
// lines before failing.
//
// A single line longer than 1 MiB fails the stream.
func (e *Executor) Lines(ctx context.Context, cmd Command, onLine func(string), opts ...Option) (Result, error) {
	cfg, err := e.configFor(opts)
	if err != nil {
		return Result{}, err
	}

	cmd = applySudo(cfg, cmd)

	return e.runWithRetry(ctx, cfg, func(int) (Result, error) {
		return e.streamOnce(ctx, cmd, onLine)
	})
}

// Upload copies a local path to the target, retrying on transport
// failures per the executor's default policy. Transfers are path-based and
// atomic, so a retry re-reads the source and never corrupts the
// destination.
func (e *Executor) Upload(ctx context.Context, localPath, remotePath string, opts ...TransferOption) error {
	return e.retryTransfer(ctx, func() error {
		return e.env.Upload(ctx, localPath, remotePath, opts...)
	})
}

// Download copies a target path to the local filesystem, with the same
// retry policy as Upload.
func (e *Executor) Download(ctx context.Context, remotePath, localPath string, opts ...TransferOption) error {
	return e.retryTransfer(ctx, func() error {
		return e.env.Download(ctx, remotePath, localPath, opts...)
	})
}

// configFor overlays per-call options on the executor's defaults.
func (e *Executor) configFor(opts []Option) (execConfig, error) {
	cfg := e.base
	for _, opt := range opts {
		opt(&cfg)
	}

	if cfg.attempts < 1 {
		return cfg, fmt.Errorf("invoke: retry attempts must be at least 1, got %d", cfg.attempts)
	}

	return cfg, nil
}

// runWithRetry drives attempt up to the configured number of times,
// retrying only on TransportError and waiting per the backoff between
// attempts. The final TransportError is returned as-is, never re-wrapped.
func (e *Executor) runWithRetry(ctx context.Context, cfg execConfig, attempt func(attempt int) (Result, error)) (Result, error) {
	var (
		res Result
		err error
	)

	for n := 1; n <= cfg.attempts; n++ {
		if n > 1 {
			if werr := e.backoffWait(ctx, cfg, n); werr != nil {
				return res, werr
			}
		}

		res, err = attempt(n)
		if err == nil || !retryable(err) {
			return res, err
		}
	}

	return res, err
}

// retryTransfer runs a transfer under the executor's default retry policy.
func (e *Executor) retryTransfer(ctx context.Context, transfer func() error) error {
	if e.base.attempts < 1 {
		return fmt.Errorf("invoke: retry attempts must be at least 1, got %d", e.base.attempts)
	}

	var err error

	for n := 1; n <= e.base.attempts; n++ {
		if n > 1 {
			if werr := e.backoffWait(ctx, e.base, n); werr != nil {
				return werr
			}
		}

		err = transfer()
		if err == nil || !retryable(err) {
			return err
		}
	}

	return err
}

// once starts cmd, waits for it, and releases the process handle.
func (e *Executor) once(ctx context.Context, cmd Command, stdio IO) (Result, error) {
	proc, err := e.env.Start(ctx, cmd, stdio)
	if err != nil {
		return Result{}, err
	}

	defer func() { _ = proc.Close() }()

	return proc.Wait()
}

// streamOnce runs cmd once, delivering each stdout line to onLine.
func (e *Executor) streamOnce(ctx context.Context, cmd Command, onLine func(string)) (Result, error) {
	pr, pw := io.Pipe()

	scanErr := make(chan error, 1)

	go func() {
		scanner := bufio.NewScanner(pr)
		scanner.Buffer(make([]byte, lineScannerStart), lineScannerMax)

		for scanner.Scan() {
			onLine(scanner.Text())
		}

		scanErr <- scanner.Err()
	}()

	proc, err := e.env.Start(ctx, cmd, IO{Stdout: pw})
	if err != nil {
		_ = pw.Close()
		_ = pr.Close()

		<-scanErr

		return Result{}, err
	}

	defer func() { _ = proc.Close() }()

	res, waitErr := proc.Wait()

	// Closing the write end signals EOF to the scanner; then drain it.
	_ = pw.Close()
	sErr := <-scanErr
	_ = pr.Close()

	switch {
	case waitErr != nil:
		return res, waitErr
	case sErr != nil:
		return res, fmt.Errorf("invoke: streaming output: %w", sErr)
	default:
		return res, nil
	}
}

// backoffWait sleeps for the configured backoff before attempt n,
// returning the context error if it is canceled first.
func (e *Executor) backoffWait(ctx context.Context, cfg execConfig, n int) error {
	if cfg.backoff == nil {
		return ctx.Err()
	}

	delay := cfg.backoff(n)
	if delay <= 0 {
		return ctx.Err()
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// retryable reports whether an error is a transport failure — the only
// family the executor retries. Everything else (exit errors, cancellation,
// closed environments, unsupported features, missing binaries) is
// terminal, so retrying must be earned by explicit classification.
func retryable(err error) bool {
	var te *TransportError

	return errors.As(err, &te)
}

// applySudo rewrites cmd to run through sudo, or returns it unchanged when
// no sudo policy is configured. Arguments are passed as an argv after a --
// separator, so nothing can be misread as a flag or interpreted by a
// shell.
func applySudo(cfg execConfig, cmd Command) Command {
	if cfg.sudo == nil {
		return cmd
	}

	args := []string{"-n"}
	if cfg.sudo.user != "" {
		args = append(args, "-u", cfg.sudo.user)
	}

	if cfg.sudo.group != "" {
		args = append(args, "-g", cfg.sudo.group)
	}

	if cfg.sudo.preserveEnv {
		args = append(args, "-E")
	}

	args = append(args, cfg.sudo.flags...)
	args = append(args, "--", cmd.Path)
	args = append(args, cmd.Args...)

	return Command{Path: "sudo", Args: args, Env: cmd.Env, Dir: cmd.Dir}
}

// tail returns the last n bytes of b (a copy), or all of b when shorter.
func tail(b []byte, n int) []byte {
	if len(b) <= n {
		return bytes.Clone(b)
	}

	return bytes.Clone(b[len(b)-n:])
}
