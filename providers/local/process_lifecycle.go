package local

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/ruffel/invoke"
)

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
