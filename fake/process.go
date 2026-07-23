package fake

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ruffel/invoke"
)

// session carries one invocation's streams and execution state through the
// builtin and handler machinery.
type session struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	dir    string
	env    map[string]string
	fs     *vfs
	owner  *Environment
}

func (s *session) clone() *session {
	copied := *s

	return &copied
}

// lookupEnv resolves a variable from the session environment.
func (s *session) lookupEnv(name string) string {
	return s.env[name]
}

// process is a simulated running command.
type process struct {
	env     *Environment
	started time.Time

	cancelRun context.CancelFunc
	done      chan struct{}

	// Set through finish or abandon before done is closed.
	finishOnce  sync.Once
	exitCode    int
	interrupted bool

	// Attribution, set before the corresponding cancellation.
	closedByUser atomic.Bool
	killSignal   atomic.Value // invoke.Signal
	startCtxErr  func() error

	waitOnce sync.Once
	result   invoke.Result
	waitErr  error

	closeOnce sync.Once
}

// abandonGrace is how long a terminated body gets to finish before the
// caller is released without it. It mirrors the local provider's
// default termination grace: the same wedge — a read nothing can
// interrupt — gets the same bounded patience.
const abandonGrace = 2 * time.Second

// spawn launches fn as the simulated process body.
//
// The body owns the happy path: it publishes its outcome and releases
// its context registration when it returns. A watcher covers the path
// where the body cannot return — wedged in a caller-supplied Read that
// nothing in Go can interrupt — by abandoning it a grace period after
// its context is canceled, so Wait, Signal, and Close stay bounded.
func (e *Environment) spawn(ctx context.Context, guard *outputGuard, fn func(runCtx context.Context) (int, bool)) *process {
	runCtx, cancel := context.WithCancel(ctx)

	p := &process{
		env:         e,
		started:     time.Now(),
		cancelRun:   cancel,
		done:        make(chan struct{}),
		startCtxErr: ctx.Err,
	}

	e.track(p)

	go func() {
		// Releasing the context registration on return keeps a
		// long-lived parent context from accumulating one child per
		// completed command.
		defer cancel()

		// The deferred publish reports a panicking body as interrupted
		// rather than leaving Wait blocked forever.
		code, interrupted := -1, true

		defer func() { p.finish(code, interrupted) }()

		code, interrupted = fn(runCtx)
	}()

	go func() {
		select {
		case <-p.done:
		case <-runCtx.Done():
			timer := time.NewTimer(abandonGrace)
			defer timer.Stop()

			select {
			case <-p.done:
			case <-timer.C:
				p.abandon(guard)
			}
		}
	}()

	return p
}

// Wait blocks until the simulated process finishes and returns its
// outcome; repeated calls return the cached result.
func (p *process) Wait() (invoke.Result, error) {
	p.waitOnce.Do(func() {
		<-p.done
		p.env.untrack(p)

		duration := time.Since(p.started)
		p.result, p.waitErr = p.mapOutcome(duration)
	})

	return p.result, p.waitErr
}

// Signal delivers sig to the simulated process: the supported set
// terminates it with the signal recorded; anything else is refused.
func (p *process) Signal(sig invoke.Signal) error {
	if p.closedByUser.Load() {
		return fmt.Errorf("fake: signal: %w", invoke.ErrClosed)
	}

	select {
	case <-p.done:
		return errors.New("fake: signal: process has exited")
	default:
	}

	if !deliverableSignal(sig) {
		return fmt.Errorf("fake: signal %s: %w", sig, invoke.ErrNotSupported)
	}

	p.killSignal.Store(sig)
	p.cancelRun()

	return nil
}

// Close terminates the simulated process if still running and caches the
// outcome. It is idempotent.
func (p *process) Close() error {
	p.closeOnce.Do(func() {
		select {
		case <-p.done:
		default:
			p.closedByUser.Store(true)
			p.cancelRun()
		}

		_, _ = p.Wait()
	})

	return nil
}

func (p *process) mapOutcome(duration time.Duration) (invoke.Result, error) {
	if !p.interrupted {
		if p.exitCode != 0 {
			return invoke.Result{ExitCode: p.exitCode, Duration: duration},
				&invoke.ExitError{Code: p.exitCode}
		}

		return invoke.Result{ExitCode: 0, Duration: duration}, nil
	}

	if p.closedByUser.Load() {
		return invoke.Result{ExitCode: -1, Duration: duration},
			fmt.Errorf("fake: wait: process terminated by Close: %w", invoke.ErrClosed)
	}

	if sig, ok := p.killSignal.Load().(invoke.Signal); ok {
		return invoke.Result{ExitCode: -1, Duration: duration},
			&invoke.ExitError{Code: -1, Signal: sig}
	}

	if err := p.startCtxErr(); err != nil {
		return invoke.Result{ExitCode: -1, Duration: duration}, fmt.Errorf("fake: wait: %w", err)
	}

	return invoke.Result{ExitCode: -1, Duration: duration},
		errors.New("fake: wait: process interrupted for an unknown reason")
}

// finish publishes the body's own outcome, once.
func (p *process) finish(code int, interrupted bool) {
	p.finishOnce.Do(func() {
		p.exitCode = code
		p.interrupted = interrupted

		close(p.done)
	})
}

// abandon releases the caller from a body that did not stop when told
// to. The writers are silenced first, so a goroutine that later comes
// back to life cannot write into buffers whose owner was told the
// process is over. Attribution is unaffected: whoever canceled the run
// context recorded why before doing so.
func (p *process) abandon(guard *outputGuard) {
	p.finishOnce.Do(func() {
		guard.silence()

		p.exitCode = -1
		p.interrupted = true

		close(p.done)
	})
}

// deliverableSignal reports whether the fake target delivers sig; the
// whole supported set default-terminates a simulated process.
func deliverableSignal(sig invoke.Signal) bool {
	switch sig {
	case invoke.SIGINT, invoke.SIGTERM, invoke.SIGKILL, invoke.SIGHUP,
		invoke.SIGQUIT, invoke.SIGUSR1, invoke.SIGUSR2:
		return true
	default:
		return false
	}
}

// emptyReader is the nil-Stdin stand-in: immediate EOF.
type emptyReader struct{}

func (emptyReader) Read(_ []byte) (int, error) { return 0, io.EOF }

// outputGuard stands between a process body and the caller's writers.
// Silencing it discards all further writes, and blocks until any write
// in flight has drained — after which nothing can land in the caller's
// buffers, however late an abandoned goroutine wakes up.
type outputGuard struct {
	mu       sync.Mutex
	silenced bool
}

// wrap guards w; a nil writer is the usual discard.
func (g *outputGuard) wrap(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}

	return &guardedWriter{guard: g, w: w}
}

func (g *outputGuard) silence() {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.silenced = true
}

type guardedWriter struct {
	guard *outputGuard
	w     io.Writer
}

func (w *guardedWriter) Write(b []byte) (int, error) {
	w.guard.mu.Lock()
	defer w.guard.mu.Unlock()

	if w.guard.silenced {
		return len(b), nil
	}

	return w.w.Write(b)
}

// newSession materializes the execution state for one invocation. The
// returned guard silences the session's caller-facing writers if the
// process is abandoned.
func (e *Environment) newSession(cmd invoke.Command, stdio invoke.IO) (*session, *outputGuard) {
	env := make(map[string]string, len(e.baseEnv)+len(cmd.Env))
	maps.Copy(env, e.baseEnv)

	for _, pair := range cmd.Env {
		if key, value, ok := strings.Cut(pair, "="); ok {
			env[key] = value
		}
	}

	dir := cmd.Dir
	if dir == "" {
		dir = "/"
	}

	guard := &outputGuard{}

	s := &session{
		stdin:  stdio.Stdin,
		stdout: guard.wrap(stdio.Stdout),
		stderr: guard.wrap(stdio.Stderr),
		dir:    dir,
		env:    env,
		fs:     e.fs,
		owner:  e,
	}

	if s.stdin == nil {
		s.stdin = emptyReader{}
	}

	return s, guard
}
