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

	waitErr := proc.Wait()
	// Always return the result if available, even if Wait reports an error (e.g. non-zero exit status or transport error)
	if res := proc.Result(); res != nil {
		return res, waitErr
	}

	return nil, waitErr
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
	cmdStr := "which"
	args := []string{file}

	if e.TargetOS() == invoke.OSWindows {
		cmdStr = "where"
	}

	var stdout strings.Builder

	cmd := &invoke.Command{
		Cmd:    cmdStr,
		Args:   args,
		Stdout: &stdout,
	}

	result, err := e.Run(ctx, cmd)
	if err != nil {
		return "", err
	}

	if result.ExitCode != 0 {
		return "", &invoke.ExitError{Command: cmd, ExitCode: result.ExitCode}
	}

	// Windows 'where' might return multiple lines, take the first one
	output := strings.TrimSpace(stdout.String())
	if e.TargetOS() == invoke.OSWindows {
		lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
		if len(lines) > 0 {
			return strings.TrimSpace(lines[0]), nil
		}

		return "", &invoke.ExitError{Command: cmd, ExitCode: 1}
	}

	return output, nil
}

func (e *Environment) decrementActive() {
	e.mu.Lock()
	e.active--
	e.mu.Unlock()
}
