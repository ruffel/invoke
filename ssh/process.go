package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ruffel/invoke"
	"golang.org/x/crypto/ssh"
)

// process implements invoke.Process for a remotely started command.
type process struct {
	env     *Environment
	session *ssh.Session
	started time.Time

	closedByUser atomic.Bool
	ctxErr       func() error

	waitReturned atomic.Bool
	waitOnce     sync.Once
	result       invoke.Result
	waitErr      error

	closeOnce sync.Once
	done      chan struct{}
}

// Start launches cmd on the remote host and returns a handle to it.
func (e *Environment) Start(ctx context.Context, cmd invoke.Command, stdio invoke.IO) (invoke.Process, error) {
	if err := e.checkOpen("start"); err != nil {
		return nil, err
	}

	if err := cmd.Validate(); err != nil {
		return nil, fmt.Errorf("ssh: start: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("ssh: start: %w", err)
	}

	if stdio.TTY != nil {
		return nil, fmt.Errorf("ssh: start: tty allocation: %w", invoke.ErrNotSupported)
	}

	if err := e.preCheck(ctx, cmd); err != nil {
		return nil, err
	}

	session, err := e.client.NewSession()
	if err != nil {
		return nil, &invoke.TransportError{Op: "start", Err: err}
	}

	applyEnv(session, cmd.Env)
	session.Stdin = stdio.Stdin
	session.Stdout = stdio.Stdout
	session.Stderr = stdio.Stderr

	p := &process{
		env:     e,
		session: session,
		started: time.Now(),
		ctxErr:  ctx.Err,
		done:    make(chan struct{}),
	}

	if err := session.Start(commandLine(cmd)); err != nil {
		_ = session.Close()

		return nil, &invoke.TransportError{Op: "start", Err: err}
	}

	e.track(p)

	go p.monitorContext(ctx)

	return p, nil
}

// preCheck validates the command's working directory and executable before
// running it, so those setup failures are classified as ErrInvalidWorkdir
// or ErrNotFound rather than surfacing as a runtime exit code.
func (e *Environment) preCheck(ctx context.Context, cmd invoke.Command) error {
	_, code, err := e.runRaw(ctx, preCheckLine(cmd))
	if err != nil {
		return fmt.Errorf("ssh: start: %w", err)
	}

	switch code {
	case preCheckBadDir:
		return fmt.Errorf("ssh: start: workdir %q: %w", cmd.Dir, invoke.ErrInvalidWorkdir)
	case preCheckNotFound:
		return fmt.Errorf("ssh: start %q: %w", cmd.Path, invoke.ErrNotFound)
	default:
		return nil
	}
}

// applyEnv sends environment variables to the session out of band, so they
// are not visible in the remote process table. Servers that reject an
// AcceptEnv variable simply drop it; the request never fails the command.
func applyEnv(session *ssh.Session, env []string) {
	for _, pair := range env {
		if key, value, ok := strings.Cut(pair, "="); ok {
			_ = session.Setenv(key, value)
		}
	}
}

// Wait blocks until the remote command completes and returns its outcome.
func (p *process) Wait() (invoke.Result, error) {
	p.waitOnce.Do(p.doWait)

	return p.result, p.waitErr
}

// Signal delivers sig to the remote process by name.
func (p *process) Signal(sig invoke.Signal) error {
	if p.closedByUser.Load() {
		return fmt.Errorf("ssh: signal: %w", invoke.ErrClosed)
	}

	if p.waitReturned.Load() {
		return errors.New("ssh: signal: process has exited")
	}

	if !supportedSignal(sig) {
		return fmt.Errorf("ssh: signal %s: %w", sig, invoke.ErrNotSupported)
	}

	if err := p.session.Signal(ssh.Signal(sig)); err != nil {
		return fmt.Errorf("ssh: signal %s: %w", sig, err)
	}

	return nil
}

// Close terminates the remote command if it is still running and waits for
// the outcome to settle. It is idempotent.
func (p *process) Close() error {
	p.closeOnce.Do(func() {
		p.closedByUser.Store(true)

		if !p.waitReturned.Load() {
			_ = p.session.Signal(ssh.SIGKILL)
			_ = p.session.Close()
		}

		_, _ = p.Wait()
	})

	return nil
}

func (p *process) doWait() {
	defer close(p.done)
	defer p.env.untrack(p)

	err := p.session.Wait()
	p.waitReturned.Store(true)

	duration := time.Since(p.started)
	p.result, p.waitErr = p.mapOutcome(err, duration)

	_ = p.session.Close()
}

// monitorContext kills the remote command when the start context is
// canceled, so cancellation terminates work on the server rather than
// merely unblocking the local Wait.
func (p *process) monitorContext(ctx context.Context) {
	select {
	case <-ctx.Done():
		if !p.waitReturned.Load() {
			_ = p.session.Signal(ssh.SIGKILL)
			_ = p.session.Close()
		}
	case <-p.done:
	}
}

// mapOutcome classifies the raw session-wait error into the package
// taxonomy, attributing a caller-initiated kill to Close or cancellation
// rather than reporting it as a command exit.
func (p *process) mapOutcome(err error, duration time.Duration) (invoke.Result, error) {
	if p.closedByUser.Load() {
		return invoke.Result{ExitCode: -1, Duration: duration},
			fmt.Errorf("ssh: wait: process terminated by Close: %w", invoke.ErrClosed)
	}

	if ctxErr := p.ctxErr(); ctxErr != nil {
		return invoke.Result{ExitCode: -1, Duration: duration}, fmt.Errorf("ssh: wait: %w", ctxErr)
	}

	if err == nil {
		return invoke.Result{ExitCode: 0, Duration: duration}, nil
	}

	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		if sig := exitErr.Signal(); sig != "" {
			return invoke.Result{ExitCode: -1, Duration: duration},
				&invoke.ExitError{Code: -1, Signal: invoke.Signal(sig)}
		}

		code := exitErr.ExitStatus()

		return invoke.Result{ExitCode: code, Duration: duration}, &invoke.ExitError{Code: code}
	}

	// ExitMissingError: the command ran but the server never reported a
	// status. It is terminal — the command may have had side effects, so
	// retrying is not safe — but it is not an exit code we can trust.
	var missing *ssh.ExitMissingError
	if errors.As(err, &missing) {
		return invoke.Result{ExitCode: -1, Duration: duration},
			fmt.Errorf("ssh: wait: remote command exited without reporting a status: %w", err)
	}

	return invoke.Result{ExitCode: -1, Duration: duration}, &invoke.TransportError{Op: "wait", Err: err}
}

// runRaw runs a raw command line on a fresh session and returns its stdout
// and exit code. It backs LookPath, OS detection, and the pre-flight check.
func (e *Environment) runRaw(ctx context.Context, cmdline string) (string, int, error) {
	session, err := e.client.NewSession()
	if err != nil {
		return "", -1, &invoke.TransportError{Op: "session", Err: err}
	}

	defer func() { _ = session.Close() }()

	var out bytes.Buffer

	session.Stdout = &out

	done := make(chan error, 1)

	go func() { done <- session.Run(cmdline) }()

	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		_ = session.Close()

		<-done

		return out.String(), -1, ctx.Err()
	case runErr := <-done:
		return classifyRaw(out.String(), runErr)
	}
}

// classifyRaw turns a session.Run result into (stdout, exit code, error).
func classifyRaw(out string, err error) (string, int, error) {
	if err == nil {
		return out, 0, nil
	}

	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		return out, exitErr.ExitStatus(), nil
	}

	return out, -1, &invoke.TransportError{Op: "run", Err: err}
}

// supportedSignal reports whether sig is in the portable signal set the
// provider delivers.
func supportedSignal(sig invoke.Signal) bool {
	switch sig {
	case invoke.SIGINT, invoke.SIGTERM, invoke.SIGKILL, invoke.SIGHUP,
		invoke.SIGQUIT, invoke.SIGUSR1, invoke.SIGUSR2:
		return true
	default:
		return false
	}
}
