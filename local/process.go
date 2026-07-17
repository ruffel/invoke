package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ruffel/invoke"
)

// waitDelay bounds how long Wait can remain blocked after the process has
// exited (or its context was canceled) while its output pipes are still
// held open — for example by an orphaned descendant. os/exec cannot bound
// a blocking caller-supplied Stdin the same way (closing pipes does not
// unblock an arbitrary Reader.Read), which is why stdin is wired through
// our own pipe below rather than handed to os/exec.
const waitDelay = 2 * time.Second

// process implements invoke.Process for a locally started command.
type process struct {
	env     *Environment
	execCmd *exec.Cmd
	started time.Time

	// Attribution flags: set before the corresponding kill is issued, so
	// Wait can attribute a SIGKILL death to its actual initiator instead
	// of guessing from context state afterward.
	ctxCanceled  atomic.Bool
	closedByUser atomic.Bool
	ctxErr       atomic.Value // error recorded when the context fires

	// stdinW is the write end of the stdin pipe when the caller supplied
	// a Stdin. The child reads the pipe directly (no os/exec-managed
	// copy goroutine), so a Reader whose Read never returns cannot hang
	// Wait; our own copy goroutine is abandoned, not awaited.
	stdinW *os.File

	waitReturned atomic.Bool
	waitOnce     sync.Once
	result       invoke.Result
	waitErr      error

	closeOnce sync.Once
}

// Start launches cmd on the local machine.
func (e *Environment) Start(ctx context.Context, cmd invoke.Command, stdio invoke.IO) (invoke.Process, error) {
	if err := e.checkOpen("start"); err != nil {
		return nil, err
	}

	if err := cmd.Validate(); err != nil {
		return nil, fmt.Errorf("local: start: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("local: start: %w", err)
	}

	if stdio.TTY != nil {
		return nil, fmt.Errorf("local: start: tty allocation: %w", invoke.ErrNotSupported)
	}

	if cmd.Dir != "" && !dirExists(cmd.Dir) {
		return nil, fmt.Errorf("local: start: workdir %q: %w", cmd.Dir, invoke.ErrInvalidWorkdir)
	}

	execCmd := exec.CommandContext(ctx, cmd.Path, cmd.Args...)
	execCmd.Dir = cmd.Dir
	execCmd.Stdout = stdio.Stdout
	execCmd.Stderr = stdio.Stderr
	execCmd.WaitDelay = waitDelay

	if len(cmd.Env) > 0 {
		execCmd.Env = append(os.Environ(), cmd.Env...)
	}

	setProcessGroup(execCmd)

	p := &process{env: e, execCmd: execCmd}

	// Stdin goes through our own pipe: the child reads an *os.File, so
	// os/exec spawns no stdin goroutine and Wait can never block on a
	// Reader whose Read never returns.
	var stdinR *os.File

	if stdio.Stdin != nil {
		var err error

		stdinR, p.stdinW, err = os.Pipe()
		if err != nil {
			return nil, fmt.Errorf("local: start: stdin pipe: %w", err)
		}

		execCmd.Stdin = stdinR
	}

	// Cancellation kills the whole process group and records the cause
	// first, so Wait can attribute the death correctly. Killing an
	// already-exited process reports ErrProcessDone, which os/exec
	// treats as benign — a command that exited before cancellation
	// keeps its real outcome.
	execCmd.Cancel = func() error {
		p.ctxErr.Store(ctx.Err())
		p.ctxCanceled.Store(true)

		return p.kill()
	}

	p.started = time.Now()

	if err := execCmd.Start(); err != nil {
		if stdinR != nil {
			_ = stdinR.Close()
			_ = p.stdinW.Close()
		}

		return nil, classifyStartError(cmd, err)
	}

	if stdinR != nil {
		// The child holds its own copy of the read end.
		_ = stdinR.Close()

		go func(src io.Reader, dst *os.File) {
			_, _ = io.Copy(dst, src)
			_ = dst.Close()
		}(stdio.Stdin, p.stdinW)
	}

	e.track(p)

	return p, nil
}

// Wait blocks until the process exits and returns its outcome. It is
// idempotent: every call returns the first outcome computed.
func (p *process) Wait() (invoke.Result, error) {
	p.waitOnce.Do(p.doWait)

	return p.result, p.waitErr
}

// Signal delivers sig to the process group, so shell-wrapped commands
// receive it alongside their children, matching terminal semantics.
func (p *process) Signal(sig invoke.Signal) error {
	if p.closedByUser.Load() {
		return fmt.Errorf("local: signal: %w", invoke.ErrClosed)
	}

	if p.waitReturned.Load() {
		return errors.New("local: signal: process has exited")
	}

	sys, ok := sysSignal(sig)
	if !ok {
		return fmt.Errorf("local: signal %s: %w", sig, invoke.ErrNotSupported)
	}

	if err := signalGroup(p.execCmd.Process.Pid, sys); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return errors.New("local: signal: process has exited")
		}

		return fmt.Errorf("local: signal %s: %w", sig, err)
	}

	return nil
}

// Close terminates the process if it is still running and waits for it to
// be reaped. It is idempotent, and a process killed by Close reports an
// error wrapping invoke.ErrClosed from Wait — never an ExitError.
func (p *process) Close() error {
	p.closeOnce.Do(func() {
		p.closedByUser.Store(true)

		if !p.waitReturned.Load() {
			_ = p.kill()
		}

		_, _ = p.Wait()
	})

	return nil
}

func (p *process) doWait() {
	defer p.env.untrack(p)

	err := p.execCmd.Wait()
	p.waitReturned.Store(true)

	// Release the stdin pipe: a copy goroutine still parked in the
	// caller's Reader is abandoned (its next write fails), never
	// awaited. Double-closing when the copy already finished is benign.
	if p.stdinW != nil {
		_ = p.stdinW.Close()
	}

	duration := time.Since(p.started)
	p.result, p.waitErr = p.mapOutcome(err, duration)
}

// kill terminates the whole process group. It reports os.ErrProcessDone
// when the process has already been reaped, which callers treat as benign.
func (p *process) kill() error {
	if p.waitReturned.Load() {
		return os.ErrProcessDone
	}

	proc := p.execCmd.Process
	if proc == nil {
		return os.ErrProcessDone
	}

	return killProcessGroup(proc.Pid)
}

// mapOutcome classifies the raw wait error into the package taxonomy,
// using the attribution flags plus evidence from the process state so a
// death is credited to its actual cause.
func (p *process) mapOutcome(err error, duration time.Duration) (invoke.Result, error) {
	state := p.execCmd.ProcessState

	exitCode := -1

	var (
		sig      invoke.Signal
		signaled bool
	)

	if state != nil {
		exitCode = state.ExitCode()
		sig, signaled = exitSignal(state)
	}

	if err == nil {
		return invoke.Result{ExitCode: 0, Duration: duration}, nil
	}

	// A normal exit beats cancellation bookkeeping. When cancellation
	// races a process that already exited, the group kill can misfire in
	// platform-specific ways (a zombie's group reports EPERM on macOS,
	// or accepts the signal vacuously), and os/exec then substitutes the
	// cancellation or Cancel error for the successful wait. The process
	// state knows better: it exited on its own, so that is the outcome.
	if state != nil && state.Exited() && !signaled && (p.ctxCanceled.Load() || isContextErr(err)) {
		return p.exitOutcome(exitCode, "", false, duration)
	}

	// A SIGKILL death combined with the matching flag means our own
	// Close or cancellation kill; either flag without the evidence (the
	// process exited on its own first) leaves the real outcome intact.
	killedByUs := signaled && sig == invoke.SIGKILL

	if p.closedByUser.Load() && killedByUs {
		return invoke.Result{ExitCode: -1, Duration: duration},
			fmt.Errorf("local: wait: process terminated by Close: %w", invoke.ErrClosed)
	}

	if p.ctxCanceled.Load() && (killedByUs || isContextErr(err)) {
		return invoke.Result{ExitCode: -1, Duration: duration}, fmt.Errorf("local: wait: %w", p.cancelCause())
	}

	return p.finishedOutcome(err, exitCode, sig, signaled, duration)
}

// cancelCause returns the context error recorded when cancellation fired,
// defaulting to context.Canceled if none was captured.
func (p *process) cancelCause() error {
	if cause, ok := p.ctxErr.Load().(error); ok && cause != nil {
		return cause
	}

	return context.Canceled
}

// finishedOutcome classifies a wait error for a process whose death was
// not caller-initiated.
func (p *process) finishedOutcome(err error, exitCode int, sig invoke.Signal, signaled bool, duration time.Duration) (invoke.Result, error) {
	// ErrWaitDelay means the process exited but its I/O stayed open past
	// the grace period (an orphan holding the pipe, a blocking Stdin).
	// The process's own outcome is known and is what we report.
	if errors.Is(err, exec.ErrWaitDelay) && p.execCmd.ProcessState != nil {
		return p.exitOutcome(exitCode, sig, signaled, duration)
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return p.exitOutcome(exitCode, sig, signaled, duration)
	}

	return invoke.Result{ExitCode: -1, Duration: duration}, fmt.Errorf("local: wait: %w", err)
}

func (p *process) exitOutcome(code int, sig invoke.Signal, signaled bool, duration time.Duration) (invoke.Result, error) {
	if signaled {
		return invoke.Result{ExitCode: -1, Duration: duration}, &invoke.ExitError{Code: -1, Signal: sig}
	}

	if code == 0 {
		return invoke.Result{ExitCode: 0, Duration: duration}, nil
	}

	return invoke.Result{ExitCode: code, Duration: duration}, &invoke.ExitError{Code: code}
}

func classifyStartError(cmd invoke.Command, err error) error {
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("local: start %q: %w", cmd.Path, invoke.ErrNotFound)
	}

	return fmt.Errorf("local: start %q: %w", cmd.Path, err)
}

func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
