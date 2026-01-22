package docker

import (
	"context"
	"errors"
	"fmt"
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
func New(c Config) (*Environment, error) {
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

	if err := proc.Wait(); err != nil {
		return nil, err
	}

	return proc.Result(), nil
}

// Start spawns a command asynchronously.
func (e *Environment) Start(ctx context.Context, cmd *invoke.Command) (invoke.Process, error) {
	e.mu.Lock()

	if e.closed {
		e.mu.Unlock()

		return nil, errors.New("docker environment closed")
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

func (e *Environment) decrementActive() {
	e.mu.Lock()
	e.active--
	e.mu.Unlock()
}
