package ssh

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/ruffel/invoke"
	"golang.org/x/crypto/ssh"
)

// Process implements invoke.Process for SSH execution.
type Process struct {
	env     *Environment
	session *ssh.Session
	cmd     *invoke.Command

	result *invoke.Result
	mu     sync.RWMutex
	done   chan struct{}
	closed bool
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
		// If it's a clean exit error, convert to invoke.ExitError
		exitErr := &ssh.ExitError{}
		if errors.As(p.result.Error, &exitErr) {
			return &invoke.ExitError{
				Command:  p.cmd,
				ExitCode: exitErr.ExitStatus(),
				Cause:    p.result.Error,
			}
		}

		return p.result.Error
	}

	return nil
}

// Result returns the command execution result.
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

// Signal sends a signal to the remote process.
func (p *Process) Signal(sig os.Signal) error {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.closed || p.session == nil {
		return errors.New("process closed or not started")
	}

	// Map OS signals to SSH signals
	var sshSig ssh.Signal

	switch sig {
	case os.Interrupt:
		sshSig = ssh.SIGINT
	case os.Kill:
		sshSig = ssh.SIGKILL
	default:
		return fmt.Errorf("signal %v not supported over ssh", sig)
	}

	return p.session.Signal(sshSig)
}

// Close terminates the SSH session.
func (p *Process) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil
	}

	p.closed = true

	if p.session != nil {
		return p.session.Close()
	}

	return nil
}

func (p *Process) start(ctx context.Context) error {
	if p.cmd.Stdout != nil {
		p.session.Stdout = p.cmd.Stdout
	}

	if p.cmd.Stderr != nil {
		p.session.Stderr = p.cmd.Stderr
	}

	if p.cmd.Stdin != nil {
		p.session.Stdin = p.cmd.Stdin
	}

	isWindows := p.env.TargetOS() == invoke.OSWindows

	if p.cmd.Tty {
		modes := buildTerminalModes()

		err := p.session.RequestPty("xterm", 80, 40, modes)
		if err != nil {
			return fmt.Errorf("request for pty failed: %w", err)
		}
	}

	startTime := time.Now()

	// Prepend env and dir to the command
	// Format: [vars] [cd] [cmd]
	// Example: VAR=1 cd /tmp && echo hello
	fullCommand := buildFullCommand(p.cmd, isWindows)

	err := p.session.Start(fullCommand)
	if err != nil {
		return err
	}

	go func() {
		defer close(p.done)
		defer p.env.decrementActive()

		// Monitor context cancellation
		doneCheck := make(chan struct{})

		go func() {
			select {
			case <-ctx.Done():
				// Context canceled: kill the session
				_ = p.Signal(os.Kill)
				_ = p.Close()
			case <-doneCheck:
				// Process finished naturally, stop monitor
			}
		}()

		err := p.session.Wait()

		close(doneCheck) // Signal monitor to exit

		duration := time.Since(startTime)

		var exitCode int

		if err != nil {
			exitErr := &ssh.ExitError{}
			if errors.As(err, &exitErr) {
				exitCode = exitErr.ExitStatus()
			} else {
				exitCode = 255 // Unknown/connection error
			}
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
