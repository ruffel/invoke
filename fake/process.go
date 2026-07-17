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

	// Set by the run goroutine before closing done.
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

// spawn launches fn as the simulated process body.
func (e *Environment) spawn(ctx context.Context, fn func(runCtx context.Context) (int, bool)) *process {
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
		defer close(p.done)

		p.exitCode, p.interrupted = fn(runCtx)
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

// newSession materializes the execution state for one invocation.
func (e *Environment) newSession(cmd invoke.Command, stdio invoke.IO) *session {
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

	s := &session{
		stdin:  stdio.Stdin,
		stdout: stdio.Stdout,
		stderr: stdio.Stderr,
		dir:    dir,
		env:    env,
		fs:     e.fs,
	}

	if s.stdin == nil {
		s.stdin = emptyReader{}
	}

	if s.stdout == nil {
		s.stdout = io.Discard
	}

	if s.stderr == nil {
		s.stderr = io.Discard
	}

	return s
}
