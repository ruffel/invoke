package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/ruffel/invoke"
)

// process implements invoke.Process for a command running in a container.
type process struct {
	env     *Environment
	execID  string
	pidFile string
	started time.Time

	attach  types.HijackedResponse
	copied  chan struct{}
	copyErr error

	// tty records that a terminal was allocated, which changes both the
	// framing of the daemon's stream and where stderr goes.
	tty bool

	closedByUser atomic.Bool
	ctxErr       func() error

	// sentSignal records a signal this process actually delivered, which
	// is the only evidence available that an exit was a signal death.
	sentSignal atomic.Value

	waitReturned atomic.Bool
	waitOnce     sync.Once
	result       invoke.Result
	waitErr      error

	closeOnce sync.Once
	done      chan struct{}
}

// Start runs cmd in the container and returns a handle to it.
func (e *Environment) Start(ctx context.Context, cmd invoke.Command, stdio invoke.IO) (invoke.Process, error) {
	if err := e.checkOpen("start"); err != nil {
		return nil, err
	}

	if err := cmd.Validate(); err != nil {
		return nil, fmt.Errorf("docker: start: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("docker: start: %w", err)
	}

	if err := e.preCheck(ctx, cmd); err != nil {
		return nil, err
	}

	argv, pidFile := e.wrapCommand(cmd)

	execID, err := e.createExec(ctx, cmd, argv, stdio.TTY)
	if err != nil {
		return nil, err
	}

	attach, err := e.client.ContainerExecAttach(ctx, execID, container.ExecAttachOptions{Tty: stdio.TTY != nil})
	if err != nil {
		return nil, classifyStart(err)
	}

	p := &process{
		env:     e,
		execID:  execID,
		pidFile: pidFile,
		started: time.Now(),
		attach:  attach,
		tty:     stdio.TTY != nil,
		copied:  make(chan struct{}),
		ctxErr:  ctx.Err,
		done:    make(chan struct{}),
	}

	e.track(p)

	go p.pumpStdin(stdio.Stdin)
	go p.pumpOutput(stdio)
	go p.monitorContext(ctx)

	return p, nil
}

// preCheck validates the command's working directory and executable
// before running it.
//
// The daemon accepts both without complaint and reports the failure only
// once the command is under way: as a diagnostic on the command's own
// stderr, under exit status 127. That status is indistinguishable from a
// shell reporting its own command-not-found, and the diagnostic would
// reach the caller as though the command had written it. Checking first
// keeps both out of the caller's way and names the problem at Start.
//
// The check needs a shell; without one the daemon's own behaviour stands,
// and the condition surfaces as exit status 127.
func (e *Environment) preCheck(ctx context.Context, cmd invoke.Command) error {
	if !e.hasShell {
		return nil
	}

	script := `if [ -n "$1" ] && ! cd "$1"; then exit 91; fi
command -v "$2" >/dev/null 2>&1 || exit 92`

	_, code, err := e.runRaw(ctx, []string{"sh", "-c", script, "sh", cmd.Dir, cmd.Path})
	if err != nil {
		return fmt.Errorf("docker: start: %w", err)
	}

	switch code {
	case preCheckBadDir:
		return fmt.Errorf("docker: start: workdir %q: %w", cmd.Dir, invoke.ErrInvalidWorkdir)
	case preCheckNotFound:
		return fmt.Errorf("docker: start %q: %w", cmd.Path, invoke.ErrNotFound)
	default:
		return nil
	}
}

// Exit codes the pre-flight check uses to tell its two failures apart.
// They sit above the range a normal command would plausibly use.
const (
	preCheckBadDir   = 91
	preCheckNotFound = 92
)

// createExec registers the command with the daemon.
//
// A terminal merges the command's two output streams into one, so stderr
// is not attached separately when one is requested; there is nothing left
// on it to carry.
func (e *Environment) createExec(ctx context.Context, cmd invoke.Command, argv []string, tty *invoke.TTY) (string, error) {
	opts := container.ExecOptions{
		User:         e.cfg.User,
		Privileged:   e.cfg.Privileged,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: tty == nil,
		Env:          cmd.Env,
		WorkingDir:   cmd.Dir,
		Cmd:          argv,
	}

	if tty != nil {
		cols, rows := tty.Size()
		opts.Tty = true
		opts.ConsoleSize = &[2]uint{uint(rows), uint(cols)} //nolint:gosec // Terminal dimensions are small positives.
	}

	resp, err := e.client.ContainerExecCreate(ctx, e.id, opts)
	if err != nil {
		return "", classifyStart(err)
	}

	return resp.ID, nil
}

// wrapCommand renders the argument vector to execute, and the path of the
// file the wrapper writes its process id to.
//
// Signals need the id of the process as the container sees it. The
// daemon reports the host's view, which names a different process inside
// the container's namespace — the classic way to signal the wrong thing.
// A shell records its own id and then replaces itself with the command,
// so the recorded id is the command's own.
func (e *Environment) wrapCommand(cmd invoke.Command) ([]string, string) {
	if !e.hasShell {
		return append([]string{cmd.Path}, cmd.Args...), ""
	}

	pidFile := "/tmp/.invoke-pid-" + randomSuffix()

	// $$ is the shell's own id, and exec replaces the shell with the
	// command, so the command inherits it.
	script := `echo $$ > "$1"; shift; exec "$@"`

	argv := append([]string{"sh", "-c", script, "sh", pidFile, cmd.Path}, cmd.Args...)

	return argv, pidFile
}

// Wait blocks until the command completes and returns its outcome.
func (p *process) Wait() (invoke.Result, error) {
	p.waitOnce.Do(p.doWait)

	return p.result, p.waitErr
}

// Signal delivers sig to the command inside the container.
func (p *process) Signal(sig invoke.Signal) error {
	if p.closedByUser.Load() {
		return fmt.Errorf("docker: signal: %w", invoke.ErrClosed)
	}

	if p.waitReturned.Load() {
		return errors.New("docker: signal: process has exited")
	}

	if !p.env.hasShell || p.pidFile == "" {
		return fmt.Errorf("docker: signal %s: container has no shell: %w", sig, invoke.ErrNotSupported)
	}

	if !supportedSignal(sig) {
		return fmt.Errorf("docker: signal %s: %w", sig, invoke.ErrNotSupported)
	}

	if err := p.kill(sig); err != nil {
		return fmt.Errorf("docker: signal %s: %w", sig, err)
	}

	p.sentSignal.Store(sig)

	return nil
}

// Close terminates the command if it is still running and waits for the
// outcome to settle. It is idempotent.
func (p *process) Close() error {
	p.closeOnce.Do(func() {
		p.closedByUser.Store(true)

		if !p.waitReturned.Load() {
			_ = p.kill(invoke.SIGKILL)
		}

		_, _ = p.Wait()
	})

	return nil
}

// kill signals the command by the id its wrapper recorded.
//
// The kill runs on a context detached from the caller's: it is most
// needed exactly when the caller's context has just been canceled, and a
// canceled context would make the daemon refuse the request that performs
// the termination.
func (p *process) kill(sig invoke.Signal) error {
	if p.pidFile == "" {
		return fmt.Errorf("signal delivery: %w", invoke.ErrNotSupported)
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), p.env.cfg.timeout())
	defer cancel()

	// The wrapper writes the file before exec'ing, so it may not exist
	// for the first instants of the command's life; wait briefly for it
	// rather than reporting a kill that signalled nothing.
	script := `for _ in 1 2 3 4 5 6 7 8 9 10; do
	if [ -s "$2" ]; then exec kill -s "$1" "$(cat "$2")"; fi
	sleep 0.05
done
exit 1`

	_, code, err := p.env.runRaw(ctx, []string{"sh", "-c", script, "sh", string(sig), p.pidFile})
	if err != nil {
		return err
	}

	if code != 0 {
		return errors.New("the command's process id was never recorded")
	}

	return nil
}

// pumpStdin forwards the caller's input and then closes the write side,
// so a command reading to EOF is not left waiting.
func (p *process) pumpStdin(stdin io.Reader) {
	if stdin != nil {
		_, _ = io.Copy(p.attach.Conn, stdin)
	}

	_ = p.attach.CloseWrite()
}

// pumpOutput demultiplexes the attached stream into the caller's writers.
// Without a terminal the daemon frames stdout and stderr on one
// connection; stdcopy separates them.
func (p *process) pumpOutput(stdio invoke.IO) {
	defer close(p.copied)

	stdout := stdio.Stdout
	if stdout == nil {
		stdout = io.Discard
	}

	// Under a terminal the daemon sends the command's bytes as they are,
	// with stderr already merged into them; without one it frames the two
	// streams together and stdcopy separates them again.
	if p.tty {
		if _, err := io.Copy(stdout, p.attach.Reader); err != nil && !errors.Is(err, io.EOF) {
			p.copyErr = err
		}

		return
	}

	stderr := stdio.Stderr
	if stderr == nil {
		stderr = io.Discard
	}

	_, err := stdcopy.StdCopy(stdout, stderr, p.attach.Reader)
	if err != nil && !errors.Is(err, io.EOF) {
		p.copyErr = err
	}
}

// doWait collects the outcome once the output stream has ended, which is
// the daemon's signal that the command finished.
func (p *process) doWait() {
	defer close(p.done)
	defer p.env.untrack(p)
	defer p.attach.Close()

	// The attached stream ends when the command exits, so waiting on the
	// copy is the wait; polling would only add latency and a cap to
	// exceed.
	<-p.copied

	duration := time.Since(p.started)
	p.result, p.waitErr = p.outcome(duration)
	p.waitReturned.Store(true)

	p.cleanupPIDFile()
}

// outcome asks the daemon for the exit status and folds it into the
// package taxonomy.
func (p *process) outcome(duration time.Duration) (invoke.Result, error) {
	if p.closedByUser.Load() {
		return invoke.Result{ExitCode: -1, Duration: duration},
			fmt.Errorf("docker: wait: process terminated by Close: %w", invoke.ErrClosed)
	}

	if ctxErr := p.ctxErr(); ctxErr != nil {
		return invoke.Result{ExitCode: -1, Duration: duration}, fmt.Errorf("docker: wait: %w", ctxErr)
	}

	inspect, err := p.inspect()
	if err != nil {
		return invoke.Result{ExitCode: -1, Duration: duration}, err
	}

	if p.copyErr != nil {
		return invoke.Result{ExitCode: -1, Duration: duration},
			&invoke.TransportError{Op: "wait", Err: p.copyErr}
	}

	if inspect.ExitCode == 0 {
		return invoke.Result{ExitCode: 0, Duration: duration}, nil
	}

	if sig, ok := p.signalDeath(inspect.ExitCode); ok {
		return invoke.Result{ExitCode: -1, Duration: duration},
			&invoke.ExitError{Code: -1, Signal: sig}
	}

	return invoke.Result{ExitCode: inspect.ExitCode, Duration: duration},
		&invoke.ExitError{Code: inspect.ExitCode}
}

// inspect reports the finished command's status, allowing for the daemon
// briefly still calling it running after the stream has closed.
func (p *process) inspect() (container.ExecInspect, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), p.env.cfg.timeout())
	defer cancel()

	const settleInterval = 5 * time.Millisecond

	for {
		inspect, err := p.env.client.ContainerExecInspect(ctx, p.execID)
		if err != nil {
			return container.ExecInspect{}, &invoke.TransportError{Op: "inspect", Err: err}
		}

		if !inspect.Running {
			return inspect, nil
		}

		select {
		case <-ctx.Done():
			// Report the wait as unresolved rather than inventing a
			// status: an exit code of zero beside an error is how a
			// failure gets mistaken for success.
			return container.ExecInspect{}, &invoke.TransportError{
				Op:  "inspect",
				Err: errors.New("the daemon still reports the command running after its output ended"),
			}
		case <-time.After(settleInterval):
		}
	}
}

// monitorContext terminates the command when the start context is
// canceled, so cancellation stops work in the container rather than
// merely unblocking the local Wait.
func (p *process) monitorContext(ctx context.Context) {
	select {
	case <-ctx.Done():
		if !p.waitReturned.Load() {
			// Deliberately not this ctx: it has just been canceled, and
			// the kill is what makes the cancellation mean anything.
			//nolint:contextcheck // kill detaches by design; see its comment.
			_ = p.kill(invoke.SIGKILL)
		}
	case <-p.done:
	}
}

// cleanupPIDFile removes the wrapper's bookkeeping from the container.
func (p *process) cleanupPIDFile() {
	if p.pidFile == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), p.env.cfg.timeout())
	defer cancel()

	// Best effort: the container may already be gone, and a stale file
	// under /tmp is harmless next to failing a completed command.
	if _, _, err := p.env.runRaw(ctx, []string{"rm", "-f", p.pidFile}); err != nil {
		return
	}
}

// runRaw runs argv in the container and returns its combined output and
// exit code. It backs path lookup, shell detection, and signal delivery.
func (e *Environment) runRaw(ctx context.Context, argv []string) (string, int, error) {
	resp, err := e.client.ContainerExecCreate(ctx, e.id, container.ExecOptions{
		AttachStdout: true,
		AttachStderr: true,
		User:         e.cfg.User,
		Cmd:          argv,
	})
	if err != nil {
		return "", -1, &invoke.TransportError{Op: "exec", Err: err}
	}

	attach, err := e.client.ContainerExecAttach(ctx, resp.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", -1, &invoke.TransportError{Op: "exec", Err: err}
	}

	defer attach.Close()

	var stdout, stderr bytes.Buffer

	if _, err := stdcopy.StdCopy(&stdout, &stderr, attach.Reader); err != nil {
		return "", -1, &invoke.TransportError{Op: "exec", Err: err}
	}

	inspect, err := e.client.ContainerExecInspect(ctx, resp.ID)
	if err != nil {
		return stdout.String(), -1, &invoke.TransportError{Op: "inspect", Err: err}
	}

	return stdout.String(), inspect.ExitCode, nil
}

// classifyStart folds a daemon rejection into the package taxonomy. The
// daemon reports both a missing executable and an unusable working
// directory as opaque runtime failures, so they are recognized by what
// they say.
func classifyStart(err error) error {
	msg := err.Error()

	switch {
	case strings.Contains(msg, "executable file not found"),
		strings.Contains(msg, "no such file or directory"),
		strings.Contains(msg, "starting container process caused: exec"):
		return fmt.Errorf("docker: start: %w", invoke.ErrNotFound)

	case strings.Contains(msg, "no such directory"),
		strings.Contains(msg, "not a directory"),
		strings.Contains(msg, "chdir"):
		return fmt.Errorf("docker: start: workdir: %w", invoke.ErrInvalidWorkdir)

	default:
		return &invoke.TransportError{Op: "start", Err: err}
	}
}

// signalDeath reports whether an exit status is the death of a process
// this one signalled.
//
// The daemon reports a signalled command the same way it reports a
// command that chose to exit with the same number, so the status alone
// cannot distinguish them: a command exiting 143 of its own accord must
// stay an exit code. Attribution therefore rests on evidence — a signal
// this process delivered, whose number the status corroborates — rather
// than on reading any status of 128 or more as a signal.
func (p *process) signalDeath(exitCode int) (invoke.Signal, bool) {
	sent, ok := p.sentSignal.Load().(invoke.Signal)
	if !ok {
		return "", false
	}

	number, known := signalNumber(sent)
	if !known {
		return "", false
	}

	const signalExitBase = 128

	if exitCode != signalExitBase+number {
		return "", false
	}

	return sent, true
}

// linuxSignalNumbers gives each portable signal its number on Linux,
// which is what containers run.
//
//nolint:gochecknoglobals,mnd // A fixed lookup table of the kernel's own numbers.
var linuxSignalNumbers = map[invoke.Signal]int{
	invoke.SIGHUP:  1,
	invoke.SIGINT:  2,
	invoke.SIGQUIT: 3,
	invoke.SIGKILL: 9,
	invoke.SIGUSR1: 10,
	invoke.SIGUSR2: 12,
	invoke.SIGTERM: 15,
}

// signalNumber maps a portable signal name to its number on Linux, which
// is what containers run.
func signalNumber(sig invoke.Signal) (int, bool) {
	number, ok := linuxSignalNumbers[sig]

	return number, ok
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
