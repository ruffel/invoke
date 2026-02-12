package local

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/ruffel/invoke"
)

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
