package docker

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/docker/docker/client"
	"github.com/ruffel/invoke"
)

var _ invoke.Environment = (*Environment)(nil)

// Environment implements invoke.Environment for Docker.
type Environment struct {
	config Config
	client *client.Client
	mu     sync.Mutex
	active int
	closed bool
}

// New establishes a connection to the Docker daemon.
func New(opts ...Option) (*Environment, error) {
	c := Config{}
	for _, opt := range opts {
		opt(&c)
	}

	if err := c.Validate(); err != nil {
		return nil, err
	}

	cli, err := client.NewClientWithOpts(c.ClientOpts()...)
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}

	return &Environment{
		config: c,
		client: cli,
	}, nil
}

// Run executes a command synchronously.
func (e *Environment) Run(ctx context.Context, cmd *invoke.Command) (*invoke.Result, error) {
	proc, err := e.Start(ctx, cmd)
	if err != nil {
		return nil, err
	}

	defer func() { _ = proc.Close() }()

	return proc.Wait()
}

// Start spawns a command asynchronously.
func (e *Environment) Start(ctx context.Context, cmd *invoke.Command) (invoke.Process, error) {
	if err := cmd.Validate(); err != nil {
		return nil, err
	}

	e.mu.Lock()

	if e.closed {
		e.mu.Unlock()

		return nil, fmt.Errorf("cannot start command: %w", invoke.ErrEnvironmentClosed)
	}

	e.active++
	e.mu.Unlock()

	process := &Process{
		env:    e,
		client: e.client,
		cmd:    cmd,
		done:   make(chan struct{}),
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

// TargetOS returns the operating system of the container.
func (e *Environment) TargetOS() invoke.TargetOS {
	if e.config.OS == invoke.OSUnknown {
		return invoke.OSLinux
	}

	return e.config.OS
}

// Close shuts down the client connection.
func (e *Environment) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.closed {
		return nil
	}

	e.closed = true

	if e.client != nil {
		return e.client.Close()
	}

	return nil
}

// LookPath searches for an executable in the container using 'which'.
func (e *Environment) LookPath(ctx context.Context, file string) (string, error) {
	var stdout strings.Builder

	cmd := &invoke.Command{
		Cmd:    "which",
		Args:   []string{file},
		Stdout: &stdout,
	}

	result, err := e.Run(ctx, cmd)
	if err != nil {
		return "", err
	}

	if result.ExitCode != 0 {
		return "", &invoke.ExitError{Command: cmd, ExitCode: result.ExitCode}
	}

	return strings.TrimSpace(stdout.String()), nil
}

func (e *Environment) decrementActive() {
	e.mu.Lock()
	e.active--
	e.mu.Unlock()
}
