package invoketest

// The misbehave harness proves every contract can fail. It wraps the local
// provider — the reference implementation — and injects one specific
// defect at a time; the self-test matrix asserts that each contract fails
// against its defect and passes against the clean reference. A contract
// with no registered defect fails the matrix outright: a test that cannot
// fail is not a test.

import (
	"context"
	"errors"
	"io"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/local"
)

// defects enumerates the injectable misbehaviors. Each field breaks
// exactly one promise a contract enforces.
type defects struct {
	// Execution stream defects.
	truncateOutput bool // forward only the first 64 KiB of stdout
	garbleOutput   bool // prepend garbage to stdout
	mergeStreams   bool // route stderr into stdout
	swallowStdin   bool // drop the caller's Stdin entirely
	stdinNeverEOF  bool // replace nil Stdin with a never-ending reader

	// Execution semantics defects.
	dropEnv       bool // discard Command.Env
	ignoreWorkdir bool // discard Command.Dir
	exitZeroLie   bool // report success for non-zero exits
	zeroDuration  bool // report a zero Duration

	// Lifecycle defects.
	ignoreCancel      bool // detach the process from the caller's context
	exitErrorOnCancel bool // misreport cancellation as an ExitError
	closeNoOp         bool // process Close does nothing
	secondCloseErrors bool // a second Close returns an error
	closeForgets      bool // Wait after Close loses the cached outcome
	envCloseNoOp      bool // environment Close does nothing
	silentSignal      bool // Signal reports success and delivers nothing
	plainSignalError  bool // unsupported signal errors without ErrNotSupported
	waitFlipFlops     bool // a second Wait returns a different outcome
}

// misbehaveEnv wraps a real Environment and applies the configured
// defects.
type misbehaveEnv struct {
	base invoke.Environment
	d    defects
}

func (m *misbehaveEnv) Start(ctx context.Context, cmd invoke.Command, stdio invoke.IO) (invoke.Process, error) {
	if m.d.ignoreCancel {
		ctx = context.WithoutCancel(ctx)
	}

	if m.d.dropEnv {
		cmd.Env = nil
	}

	if m.d.ignoreWorkdir {
		cmd.Dir = ""
	}

	if m.d.swallowStdin {
		stdio.Stdin = nil
	}

	if m.d.stdinNeverEOF && stdio.Stdin == nil {
		stdio.Stdin = neverEOFReader{}
	}

	if m.d.mergeStreams && stdio.Stdout != nil {
		stdio.Stderr = stdio.Stdout
	}

	if m.d.truncateOutput && stdio.Stdout != nil {
		stdio.Stdout = &limitWriter{w: stdio.Stdout, limit: 64 * 1024}
	}

	if m.d.garbleOutput && stdio.Stdout != nil {
		stdio.Stdout = &garbleWriter{w: stdio.Stdout}
	}

	proc, err := m.base.Start(ctx, cmd, stdio)
	if err != nil {
		return nil, err
	}

	return &misbehaveProcess{base: proc, d: m.d}, nil
}

func (m *misbehaveEnv) LookPath(ctx context.Context, name string) (string, error) {
	return m.base.LookPath(ctx, name)
}

func (m *misbehaveEnv) Upload(ctx context.Context, localPath, remotePath string, opts ...invoke.TransferOption) error {
	return m.base.Upload(ctx, localPath, remotePath, opts...)
}

func (m *misbehaveEnv) Download(ctx context.Context, remotePath, localPath string, opts ...invoke.TransferOption) error {
	return m.base.Download(ctx, remotePath, localPath, opts...)
}

func (m *misbehaveEnv) OS() invoke.TargetOS               { return m.base.OS() }
func (m *misbehaveEnv) Capabilities() invoke.Capabilities { return m.base.Capabilities() }

func (m *misbehaveEnv) Close() error {
	if m.d.envCloseNoOp {
		return nil
	}

	return m.base.Close()
}

// misbehaveProcess applies process-level defects.
type misbehaveProcess struct {
	base       invoke.Process
	d          defects
	waitCalls  int
	closeCalls int
	closed     bool
}

func (p *misbehaveProcess) Wait() (invoke.Result, error) {
	res, err := p.base.Wait()

	p.waitCalls++
	if p.d.waitFlipFlops && p.waitCalls > 1 {
		return invoke.Result{ExitCode: res.ExitCode + 1, Duration: res.Duration}, nil
	}

	if p.d.closeForgets && p.closed {
		return invoke.Result{}, errors.New("misbehave: outcome discarded by close")
	}

	if p.d.exitZeroLie {
		var exitErr *invoke.ExitError
		if errors.As(err, &exitErr) {
			return invoke.Result{ExitCode: 0, Duration: res.Duration}, nil
		}
	}

	if p.d.exitErrorOnCancel && err != nil && errors.Is(err, context.Canceled) {
		return res, &invoke.ExitError{Code: -1}
	}

	if p.d.zeroDuration {
		res.Duration = 0
	}

	return res, err
}

func (p *misbehaveProcess) Signal(sig invoke.Signal) error {
	if p.d.silentSignal {
		return nil
	}

	if p.d.plainSignalError {
		if err := p.base.Signal(sig); err != nil {
			return errors.New("misbehave: signal failed for unstated reasons")
		}

		return nil
	}

	return p.base.Signal(sig)
}

func (p *misbehaveProcess) Close() error {
	p.closeCalls++
	p.closed = true

	if p.d.closeNoOp {
		return nil
	}

	if p.d.secondCloseErrors && p.closeCalls > 1 {
		return errors.New("misbehave: already closed")
	}

	return p.base.Close()
}

// neverEOFReader blocks forever, simulating an inherited terminal stdin.
type neverEOFReader struct{}

func (neverEOFReader) Read(_ []byte) (int, error) {
	select {} // Block until the process side gives up.
}

// garbleWriter prepends garbage to the first write, corrupting any exact
// output expectation regardless of size.
type garbleWriter struct {
	w       io.Writer
	started bool
}

func (g *garbleWriter) Write(p []byte) (int, error) {
	if !g.started {
		g.started = true

		if _, err := g.w.Write([]byte("GARBLED:")); err != nil {
			return 0, err
		}
	}

	return g.w.Write(p)
}

// limitWriter forwards at most limit bytes and silently drops the rest.
type limitWriter struct {
	w       io.Writer
	limit   int
	written int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	remain := l.limit - l.written
	if remain <= 0 {
		return len(p), nil
	}

	if len(p) > remain {
		if _, err := l.w.Write(p[:remain]); err != nil {
			return 0, err
		}

		l.written = l.limit

		return len(p), nil
	}

	n, err := l.w.Write(p)
	l.written += n

	return n, err
}

// defectCase names one injected defect and the contract it must break.
type defectCase struct {
	name     string
	contract string
	defects  defects
}

// defectCatalog maps every contract to at least one defect that must make
// it fail. The matrix enforces full coverage.
func defectCatalog() []defectCase {
	return []defectCase{
		{name: "garbled output", contract: "core/captures-stdout", defects: defects{garbleOutput: true}},
		{name: "merged streams", contract: "core/streams-stay-separate", defects: defects{mergeStreams: true}},
		{name: "stdin never EOF", contract: "core/nil-stdin-is-eof", defects: defects{stdinNeverEOF: true}},
		{name: "swallowed stdin", contract: "core/stdin-is-delivered", defects: defects{swallowStdin: true}},
		{name: "truncated large output", contract: "core/large-output-is-complete", defects: defects{truncateOutput: true}},
		{name: "dropped env", contract: "core/env-overlays-base", defects: defects{dropEnv: true}},
		{name: "ignored workdir", contract: "core/workdir-is-honored", defects: defects{ignoreWorkdir: true}},
		{name: "exit-zero lie", contract: "core/exit-code-is-reported", defects: defects{exitZeroLie: true}},
		{name: "zero duration", contract: "core/duration-is-measured", defects: defects{zeroDuration: true}},

		{name: "flip-flopping wait", contract: "lifecycle/wait-is-idempotent", defects: defects{waitFlipFlops: true}},
		{name: "ignored cancel", contract: "lifecycle/cancel-unblocks-wait", defects: defects{ignoreCancel: true}},
		{name: "cancel as exit error", contract: "lifecycle/cancel-unblocks-wait", defects: defects{exitErrorOnCancel: true}},
		{name: "surviving process", contract: "lifecycle/cancel-terminates-process", defects: defects{ignoreCancel: true}},
		{name: "start despite cancel", contract: "lifecycle/start-on-canceled-context", defects: defects{ignoreCancel: true}},
		{name: "close no-op", contract: "lifecycle/close-unblocks-wait", defects: defects{closeNoOp: true}},
		{name: "second close errors", contract: "lifecycle/close-is-idempotent", defects: defects{secondCloseErrors: true}},
		{name: "close forgets outcome", contract: "lifecycle/close-after-wait-keeps-outcome", defects: defects{closeForgets: true}},
		{name: "env close no-op", contract: "lifecycle/env-close-terminates-processes", defects: defects{envCloseNoOp: true}},
		{name: "silent signal", contract: "lifecycle/signal-terminates-process", defects: defects{silentSignal: true}},
		{name: "silent unsupported signal", contract: "lifecycle/unsupported-signal-normalized", defects: defects{silentSignal: true}},
		{name: "plain signal error", contract: "lifecycle/unsupported-signal-normalized", defects: defects{plainSignalError: true}},
	}
}

// newReferenceFactory returns a Factory over the clean local provider plus
// a cleanup that closes the underlying environments even when a defect
// makes the wrapper's own lifecycle misbehave.
func newReferenceFactory(d defects) (Factory, func()) {
	var bases []invoke.Environment

	factory := func(t T) invoke.Environment {
		env, err := local.New()
		if err != nil {
			t.Fatalf("constructing local reference environment: %v", err)
		}

		bases = append(bases, env)

		return &misbehaveEnv{base: env, d: d}
	}

	cleanup := func() {
		for _, env := range bases {
			_ = env.Close()
		}
	}

	return factory, cleanup
}
