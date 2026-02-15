package local

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/ruffel/invoke"
)

// Process implements invoke.Process for local command execution.
// It wraps `*exec.Cmd` to provide a uniform interface for waiting, signaling, and result retrieval.
type Process struct {
	env     *Environment
	cmd     *invoke.Command
	execCmd *exec.Cmd

	// Result related fields
	result *invoke.Result
	mu     sync.RWMutex
	done   chan struct{}
	closed bool
}

// Wait blocks until the command completes.
// It returns an invoke.ExitError if the command finished with a non-zero exit code,
// or a different error if the wait itself failed (e.g. context cancellation).
func (p *Process) Wait() error {
	p.mu.RLock()
	// If closed, we check if it was ever started.
	if p.closed {
		p.mu.RUnlock()

		return fmt.Errorf("cannot wait on process %q: already closed", p.cmd.String())
	}

	if p.done == nil {
		p.mu.RUnlock()

		return fmt.Errorf("cannot wait on process %q: not started", p.cmd.String())
	}

	p.mu.RUnlock()

	// Block until the monitoring goroutine closes the done channel
	<-p.done

	p.mu.RLock()
	defer p.mu.RUnlock()

	// Only return ExitError for actual exit code failures, not other errors
	if p.result.Error != nil {
		// Check if it's an exit error (non-zero exit code)
		exitErr := &exec.ExitError{}
		if errors.As(p.result.Error, &exitErr) {
			return &invoke.ExitError{
				Command:  p.cmd,
				ExitCode: exitErr.ExitCode(),
				// Note: Stderr is not captured here by default. It must be captured via Command.Stderr.
				Stderr: nil,
			}
		}
		// Return other errors (context canceled, etc.) as-is
		return p.result.Error
	}

	return nil
}

// Result returns the final metadata of the command execution.
// It returns an empty result if the process is still running or hasn't started.
// This is typically safe to call only after Wait() returns.
func (p *Process) Result() *invoke.Result {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.result == nil {
		return &invoke.Result{} // Return empty result if not ready
	}

	// Return a copy to prevent external modification
	return &invoke.Result{
		ExitCode: p.result.ExitCode,
		Duration: p.result.Duration,
		Error:    p.result.Error,
	}
}

// Signal sends an OS signal to the running process.
// It delegates directly to os.Process.Signal.
func (p *Process) Signal(sig os.Signal) error {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.closed {
		return fmt.Errorf("cannot signal process %q: already closed", p.cmd.String())
	}

	if p.execCmd == nil || p.execCmd.Process == nil {
		return fmt.Errorf("cannot signal process %q: not started", p.cmd.String())
	}

	return p.execCmd.Process.Signal(sig)
}

// Close releases resources associated with the process.
// If the process is still running, it will be killed to ensure cleanup.
func (p *Process) Close() error {
	p.mu.Lock()

	if p.closed {
		p.mu.Unlock()

		return nil // Already closed
	}

	// Determine if we need to kill the process before setting closed
	shouldKill := p.execCmd != nil && p.execCmd.Process != nil && p.done != nil
	done := p.done // Capture channel reference
	p.closed = true
	p.mu.Unlock()

	// Kill and wait outside of lock to avoid deadlock
	if shouldKill {
		select {
		case <-done:
			// Process already completed, nothing to kill
		default:
			// Process still running, kill the process group to prevent leaks.
			if p.execCmd.Process != nil && p.execCmd.Process.Pid > 0 {
				_ = killProcessGroup(p.execCmd.Process.Pid)
			}

			<-done // Wait for goroutine to finish
		}
	}

	return nil
}

// start initializes and spawns the underlying OS process.
func (p *Process) start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := validateCommand(p.cmd); err != nil {
		return err
	}

	if p.closed {
		return fmt.Errorf("cannot start process %q: already closed", p.cmd.String())
	}

	if p.cmd.Tty {
		return fmt.Errorf("cannot start process %q: %w", p.cmd.String(), invoke.ErrNotSupported)
	}

	p.execCmd = exec.CommandContext(ctx, p.cmd.Cmd, p.cmd.Args...)

	// Set working directory if specified
	if p.cmd.Dir != "" {
		p.execCmd.Dir = p.cmd.Dir
	}

	// Set environment variables if specified
	if len(p.cmd.Env) > 0 {
		p.execCmd.Env = append(os.Environ(), p.cmd.Env...)
	}

	// Create a new Process Group to allow killing the entire tree (children) later.
	setProcessGroup(p.execCmd)

	// Wire up streams.
	// If stdout/stderr are nil, os/exec will inherit parent process stdio.
	// Callers can set explicit writers to capture or redirect output.
	if p.cmd.Stdout != nil {
		p.execCmd.Stdout = p.cmd.Stdout
	}

	if p.cmd.Stderr != nil {
		p.execCmd.Stderr = p.cmd.Stderr
	}

	if p.cmd.Stdin != nil {
		p.execCmd.Stdin = p.cmd.Stdin
	}

	p.done = make(chan struct{})

	// Start the command
	startTime := time.Now()

	err := p.execCmd.Start()
	if err != nil {
		return err
	}

	// Start a goroutine to wait for completion.
	// This ensures we capture the exact exit timing and result asynchronously.
	go func() {
		defer close(p.done)
		defer p.env.decrementActive()

		err := p.execCmd.Wait()
		duration := time.Since(startTime)

		exitCode := 0
		if p.execCmd.ProcessState != nil {
			exitCode = p.execCmd.ProcessState.ExitCode()
		}

		p.mu.Lock()
		p.result = &invoke.Result{
			ExitCode: exitCode,
			Duration: duration,
			Error:    err,
		}
		p.mu.Unlock()
	}()

	return nil
}
