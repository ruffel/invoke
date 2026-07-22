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

	if err := e.preCheck(ctx, cmd); err != nil {
		return nil, err
	}

	session, err := e.client.NewSession()
	if err != nil {
		return nil, &invoke.TransportError{Op: "start", Err: err}
	}

	if err := requestPTY(session, stdio.TTY); err != nil {
		_ = session.Close()

		return nil, err
	}

	prologue, err := e.deliverEnv(ctx, session, cmd.Env)
	if err != nil {
		_ = session.Close()

		return nil, err
	}

	session.Stdin = stdio.Stdin
	session.Stdout = stdio.Stdout

	// A terminal merges the command's two output streams into one, so
	// there is nothing left for stderr to carry.
	if stdio.TTY == nil {
		session.Stderr = stdio.Stderr
	}

	p := &process{
		env:     e,
		session: session,
		started: time.Now(),
		ctxErr:  ctx.Err,
		done:    make(chan struct{}),
	}

	if err := session.Start(commandLine(cmd, prologue)); err != nil {
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

// requestPTY asks the server for a pseudo-terminal of the requested size.
//
// Echo is disabled: a terminal would otherwise reflect whatever the
// caller writes to stdin back into the output they collect, which is
// invisible until someone diffs the two.
func requestPTY(session *ssh.Session, tty *invoke.TTY) error {
	if tty == nil {
		return nil
	}

	cols, rows := tty.Size()

	modes := ssh.TerminalModes{
		ssh.ECHO:          0,
		ssh.TTY_OP_ISPEED: ttyBaudRate,
		ssh.TTY_OP_OSPEED: ttyBaudRate,
	}

	if err := session.RequestPty(defaultTerm, rows, cols, modes); err != nil {
		return &invoke.TransportError{Op: "pty", Err: err}
	}

	return nil
}

// Terminal defaults for a requested pseudo-terminal.
const (
	// defaultTerm is the terminal type reported to the remote host. A
	// command that consults it finds something it will recognize.
	defaultTerm = "xterm"

	// ttyBaudRate is the nominal line speed; it does not throttle
	// anything, but the request carries it.
	ttyBaudRate = 14400
)

// deliverEnv arranges for env to reach the command, returning any
// prologue the command line needs to carry.
//
// Variables go out of band first, where they never appear in the remote
// process table. A server accepts only those its AcceptEnv setting names,
// though, and the stock setting names none — so refusal is the ordinary
// case rather than the exception, and running the command without its
// environment is not an option.
//
// Refused variables are therefore written to a file only the login user
// can read, which the command line sources and deletes before running the
// command. The values never reach an argument vector. A caller who cannot
// use a file — a read-only target, say — can opt into carrying them on the
// command line instead, where every account on the host can read them.
func (e *Environment) deliverEnv(ctx context.Context, session *ssh.Session, env []string) (string, error) {
	var refused []string

	for _, pair := range env {
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}

		if err := session.Setenv(key, value); err != nil {
			refused = append(refused, pair)
		}
	}

	if len(refused) == 0 {
		return "", nil
	}

	if e.cfg.CommandLineEnv {
		return exportPrologue(refused), nil
	}

	path := "/tmp/.invoke-env-" + randomSuffix()

	if err := e.writeRemoteFile(ctx, path, exportScript(refused)); err != nil {
		return "", fmt.Errorf(
			"ssh: start: the server refused %s and they could not be delivered by file either; "+
				"pass WithCommandLineEnv to send them on the command line, where every account "+
				"on the host can read them: %w",
			strings.Join(refusedNames(refused), ", "), err)
	}

	return sourcePrologue(path), nil
}

// refusedNames lists the variable names from KEY=VALUE pairs, so an error
// can name them without quoting their values.
func refusedNames(pairs []string) []string {
	names := make([]string, 0, len(pairs))

	for _, pair := range pairs {
		key, _, _ := strings.Cut(pair, "=")
		names = append(names, key)
	}

	return names
}

// writeRemoteFile creates a file readable only by the login user, with
// its content carried on the command's input rather than its arguments.
func (e *Environment) writeRemoteFile(ctx context.Context, path, content string) error {
	session, err := e.client.NewSession()
	if err != nil {
		return &invoke.TransportError{Op: "session", Err: err}
	}

	defer func() { _ = session.Close() }()

	session.Stdin = strings.NewReader(content)

	done := make(chan error, 1)

	go func() {
		done <- session.Run("umask 077; cat > " + quoteArg(path))
	}()

	select {
	case <-ctx.Done():
		_ = session.Close()

		<-done

		return ctx.Err()

	case runErr := <-done:
		if runErr != nil {
			return &invoke.TransportError{Op: "write", Err: runErr}
		}

		return nil
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
//
// A status the server reported settles the outcome before the attribution
// flags are read. Cancellation and Close are things this side did, and
// neither unruns a command the server already accounted for: the status
// arrived, so the command finished, whatever this side went on to want.
// Reading the flags first turned a completed command into a cancellation
// whenever the two raced — and a caller who retries on cancellation would
// then run it again.
//
// What remains after that is a death this side could have caused: a kill
// signal, or no status at all. Those are the flags' to claim.
func (p *process) mapOutcome(err error, duration time.Duration) (invoke.Result, error) {
	sig, signaled := waitSignal(err)

	if !signaled {
		if res, outcome, reported := reportedExit(err, duration); reported {
			return res, outcome
		}
	}

	// A signal this side never sends belongs to the command: the session
	// is killed with SIGKILL and nothing else, so anything else is the
	// remote end's own doing and outlives any local bookkeeping.
	if signaled && sig != invoke.SIGKILL {
		return invoke.Result{ExitCode: -1, Duration: duration}, &invoke.ExitError{Code: -1, Signal: sig}
	}

	if p.closedByUser.Load() {
		return invoke.Result{ExitCode: -1, Duration: duration},
			fmt.Errorf("ssh: wait: process terminated by Close: %w", invoke.ErrClosed)
	}

	if ctxErr := p.ctxErr(); ctxErr != nil {
		return invoke.Result{ExitCode: -1, Duration: duration}, fmt.Errorf("ssh: wait: %w", ctxErr)
	}

	// A kill nobody here asked for is still the command's own death.
	if signaled {
		return invoke.Result{ExitCode: -1, Duration: duration}, &invoke.ExitError{Code: -1, Signal: sig}
	}

	// Every remaining failure is the same fact in a different shape: the
	// command was started and no status came back for it.
	//
	// ExitMissingError is the shape the library names, but which one an
	// outage produces is not something the caller chose — the same dying
	// connection surfaces as a missing status, a broken channel, or a
	// read error, depending on which part of the session noticed first.
	// Classifying only the named one as terminal made retryability a coin
	// flip on identical outages.
	//
	// So none of them is a TransportError, which is the retryable family.
	// The command may already have taken effect, and nothing here can tell
	// whether it did, so retrying it would be at-least-once execution of
	// an arbitrary command. File transfers classify the same outage as
	// retryable because their delivery is atomic; commands have no such
	// guarantee, and the caller must decide.
	return invoke.Result{ExitCode: -1, Duration: duration},
		fmt.Errorf("ssh: wait: the connection ended before the remote command reported a status, "+
			"so it may or may not have run to completion: %w", err)
}

// waitSignal reports the signal a session-wait error attributes the
// command's death to, and whether it named one at all.
func waitSignal(err error) (invoke.Signal, bool) {
	var exitErr *ssh.ExitError
	if !errors.As(err, &exitErr) {
		return "", false
	}

	sig := exitErr.Signal()
	if sig == "" {
		return "", false
	}

	return invoke.Signal(sig), true
}

// reportedExit returns the outcome for a session-wait error carrying an
// exit status the server sent, and whether it carried one.
func reportedExit(err error, duration time.Duration) (invoke.Result, error, bool) { //nolint:revive // The flag distinguishes "no status" from a zero one.
	if err == nil {
		return invoke.Result{ExitCode: 0, Duration: duration}, nil, true
	}

	var exitErr *ssh.ExitError
	if !errors.As(err, &exitErr) {
		return invoke.Result{}, nil, false
	}

	code := exitErr.ExitStatus()

	return invoke.Result{ExitCode: code, Duration: duration}, &invoke.ExitError{Code: code}, true
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
