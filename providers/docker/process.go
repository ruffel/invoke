package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/ruffel/invoke"
)

// Process implements invoke.Process for Docker execution.
// It manages the lifecycle of a `docker exec` session.
type Process struct {
	env    *Environment
	client *client.Client
	cmd    *invoke.Command

	execID string
	stream types.HijackedResponse

	result *invoke.Result
	mu     sync.RWMutex
	done   chan struct{}
	closed bool
}

// writerOrDiscard returns the given writer, or io.Discard if nil.
func writerOrDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}

	return w
}

// copyOutput copies the process output streams to the configured writers.
// In TTY mode, stdout/stderr are merged. In non-TTY mode, they are multiplexed.
func copyOutput(stream types.HijackedResponse, stdout, stderr io.Writer, tty bool) {
	if tty {
		// In TTY mode, stdout/stderr are merged and it's a raw stream
		_, _ = io.Copy(writerOrDiscard(stdout), stream.Reader)
	} else {
		// In non-TTY mode, it's multiplexed. Use StdCopy.
		_, _ = stdcopy.StdCopy(writerOrDiscard(stdout), writerOrDiscard(stderr), stream.Reader)
	}
}

// pollForExitCode polls the Docker API until the exec process exits or times out.
// Returns the final ExecInspect result and any error encountered.
func pollForExitCode(ctx context.Context, cli *client.Client, execID string, timeout time.Duration) (container.ExecInspect, error) {
	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		inspectResp, err := cli.ContainerExecInspect(pollCtx, execID)
		if err != nil {
			return inspectResp, err
		}

		if !inspectResp.Running {
			return inspectResp, nil
		}

		select {
		case <-pollCtx.Done():
			return inspectResp, pollCtx.Err()
		case <-ticker.C:
			// Continue polling
		}
	}
}

// Wait blocks until the command completes.
func (p *Process) Wait() error {
	p.mu.RLock()

	if p.closed {
		p.mu.RUnlock()

		return errors.New("process closed")
	}

	p.mu.RUnlock()

	<-p.done

	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.result.Error != nil {
		return p.result.Error
	}

	if p.result.ExitCode != 0 {
		return &invoke.ExitError{
			Command:  p.cmd,
			ExitCode: p.result.ExitCode,
		}
	}

	return nil
}

// Result returns the command result (only valid after Wait completes).
func (p *Process) Result() *invoke.Result {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.result == nil {
		return &invoke.Result{}
	}

	return &invoke.Result{
		ExitCode: p.result.ExitCode,
		Duration: p.result.Duration,
		Error:    p.result.Error,
	}
}

// Signal sends a signal to the process.
// Note: Docker does not natively support signaling 'exec' processes via the API directly in all versions.
// We implement this by running a separate `kill -SIGNAL PID` command inside the container.
func (p *Process) Signal(_ os.Signal) error {
	// Docker API does not support native POSIX signaling for Exec processes well.
	// We force close the hijacked connection to emulate a hangup/termination.
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.closed {
		return errors.New("process closed")
	}

	if p.stream.Conn != nil {
		p.stream.Close()

		return nil
	}

	return nil
}

// Close disconnects the stream and cleans up resources.
func (p *Process) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil
	}

	p.closed = true

	if p.stream.Conn != nil {
		p.stream.Close()
	}

	return nil
}

func (p *Process) start(ctx context.Context) error {
	execConfig := buildExecConfig(p.cmd)

	idResp, err := p.client.ContainerExecCreate(ctx, p.env.config.ContainerID, execConfig)
	if err != nil {
		return fmt.Errorf("failed to create exec: %w", err)
	}

	p.execID = idResp.ID

	attachConfig := buildAttachConfig(p.cmd)

	resp, err := p.client.ContainerExecAttach(ctx, p.execID, attachConfig)
	if err != nil {
		return fmt.Errorf("failed to attach exec: %w", err)
	}

	p.stream = resp

	startTime := time.Now()

	// Start input copier if stdin provided
	if p.cmd.Stdin != nil {
		go func() {
			defer func() { _ = p.stream.CloseWrite() }()

			_, _ = io.Copy(p.stream.Conn, p.cmd.Stdin)
		}()
	}

	// Start output copier
	outputDone := make(chan struct{})

	go func() {
		defer close(outputDone)

		copyOutput(p.stream, p.cmd.Stdout, p.cmd.Stderr, p.cmd.Tty)
	}()

	// Wait in background and poll for exit code
	go func() {
		defer close(p.done)
		defer p.env.decrementActive()
		defer p.stream.Close()

		// Context cancellation handler
		doneCheck := make(chan struct{})

		go func() {
			select {
			case <-ctx.Done():
				_ = p.Signal(os.Kill)
				_ = p.Close()
			case <-doneCheck:
			}
		}()

		// Wait for output streams to finish
		<-outputDone
		close(doneCheck)

		// Poll for exit code with fresh context (original may be cancelled)
		inspectResp, err := pollForExitCode(context.Background(), p.client, p.execID, 30*time.Second) //nolint:contextcheck
		duration := time.Since(startTime)

		p.mu.Lock()
		p.result = &invoke.Result{
			ExitCode: inspectResp.ExitCode,
			Duration: duration,
			Error:    err,
		}
		p.mu.Unlock()
	}()

	return nil
}
