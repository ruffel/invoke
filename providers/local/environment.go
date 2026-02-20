package local

import (
	"context"
	"fmt"
	"os/exec"
	"sync"

	"github.com/ruffel/invoke"
)

var _ invoke.Environment = (*Environment)(nil)

// Environment implements invoke.Environment for the local operating system.
// Thread-safe wrapper around os/exec.
type Environment struct {
	targetOS invoke.TargetOS
	mu       sync.RWMutex
	active   int
	closed   bool
}

// New creates a new local environment.
func New(opts ...Option) (*Environment, error) {
	cfg := Config{
		targetOS: invoke.DetectLocalOS(),
	}

	for _, opt := range opts {
		opt(&cfg)
	}

	return &Environment{
		targetOS: cfg.targetOS,
	}, nil
}

// Run executes a command synchronously on the local machine.
func (e *Environment) Run(ctx context.Context, cmd *invoke.Command) (*invoke.Result, error) {
	if err := cmd.Validate(); err != nil {
		return nil, err
	}

	process, err := e.Start(ctx, cmd)
	if err != nil {
		return nil, err
	}

	defer func() { _ = process.Close() }()

	waitErr := process.Wait()
	// Always return the result if available, even if Wait failed (e.g. non-zero exit code)
	if res := process.Result(); res != nil {
		return res, waitErr
	}

	return nil, waitErr
}

// Start begins command execution asynchronously.
// Caller must close/wait on the returned Process.
func (e *Environment) Start(ctx context.Context, cmd *invoke.Command) (invoke.Process, error) {
	if err := cmd.Validate(); err != nil {
		return nil, err
	}

	e.mu.Lock()

	if e.closed {
		e.mu.Unlock()

		return nil, fmt.Errorf("cannot start command %q: %w", cmd.String(), invoke.ErrEnvironmentClosed)
	}

	e.active++
	e.mu.Unlock()

	process := &Process{
		env: e,
		cmd: cmd,
	}

	err := process.start(ctx)
	if err != nil {
		e.mu.Lock()
		e.active--
		e.mu.Unlock()

		return nil, err
	}

	return process, nil
}

// TargetOS returns the operating system of the host machine.
func (e *Environment) TargetOS() invoke.TargetOS {
	return e.targetOS
}

// ActiveProcesses returns the number of currently running commands.
func (e *Environment) ActiveProcesses() int {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return e.active
}

// Close shuts down the environment.
// New Start calls will fail. Existing processes prevent active count from dropping to zero until finished.
func (e *Environment) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.closed = true

	return nil
}

// LookPath searches for an executable named file in the directories named by
// the PATH environment variable.
func (e *Environment) LookPath(_ context.Context, file string) (string, error) {
	if e.isClosed() {
		return "", fmt.Errorf("cannot look up path: %w", invoke.ErrEnvironmentClosed)
	}

	return exec.LookPath(file)
}

func (e *Environment) decrementActive() {
	e.mu.Lock()
	e.active--
	e.mu.Unlock()
}

func (e *Environment) isClosed() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return e.closed
}
