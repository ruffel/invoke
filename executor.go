package invoke

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// Executor handles command execution with retry logic, sudo support, and output buffering.
type Executor struct {
	env Environment
}

// NewExecutor creates a new Executor with the given environment.
func NewExecutor(env Environment) *Executor {
	return &Executor{env: env}
}

// Run executes a command, respecting context cancellation and configured retry policies.
func (e *Executor) Run(ctx context.Context, cmd *Command, opts ...ExecOption) (*Result, error) {
	cfg := ExecConfig{RetryAttempts: 1}

	// Apply options
	for _, o := range opts {
		o(&cfg)
	}

	if cfg.Sudo {
		cmd = e.applySudo(cmd)
	}

	var (
		lastRes *Result
		lastErr error
	)

	for i := range cfg.RetryAttempts {
		if i > 0 {
			err := e.wait(ctx, cfg.RetryDelay)
			if err != nil {
				return nil, err
			}
		}

		lastRes, lastErr = e.env.Run(ctx, cmd)

		// Check for success
		if lastErr == nil && (lastRes == nil || lastRes.ExitCode == 0) {
			return lastRes, nil
		}
	}

	if lastErr != nil {
		return lastRes, fmt.Errorf("command execution failed after %d attempts: %w", cfg.RetryAttempts, lastErr)
	}

	// Check if the final execution had a non-zero exit code
	if lastRes != nil && lastRes.ExitCode != 0 {
		return lastRes, &ExitError{
			Command:  cmd,
			ExitCode: lastRes.ExitCode,
		}
	}

	return lastRes, nil
}

// RunBuffered executes a command and captures both Stdout and Stderr.
func (e *Executor) RunBuffered(ctx context.Context, cmd *Command, opts ...ExecOption) (*BufferedResult, error) {
	var stdoutBuf, stderrBuf bytes.Buffer

	cmdCopy := *cmd // copy
	cmdCopy.Stdout = &stdoutBuf
	cmdCopy.Stderr = &stderrBuf

	result, err := e.Run(ctx, &cmdCopy, opts...)

	bufResult := &BufferedResult{
		Stdout: stdoutBuf.Bytes(),
		Stderr: stderrBuf.Bytes(),
	}
	if result != nil {
		bufResult.Result = *result
	}

	// Attach stderr to ExitError for context
	if err != nil {
		var exitErr *ExitError
		if errors.As(err, &exitErr) {
			exitErr.Stderr = bufResult.Stderr
		}
	}

	return bufResult, err
}

// RunShell executes a shell command string using the target OS's default shell.
func (e *Executor) RunShell(ctx context.Context, cmdStr string, opts ...ExecOption) (*BufferedResult, error) {
	cmd := e.env.TargetOS().ShellCommand(cmdStr)

	return e.RunBuffered(ctx, cmd, opts...)
}

// LookPath resolves an executable path using the underlying environment's LookPath strategy.
func (e *Executor) LookPath(ctx context.Context, file string) (string, error) {
	return e.env.LookPath(ctx, file)
}

// Start initiates a command asynchronously.
// Caller is responsible for Process.Wait().
func (e *Executor) Start(ctx context.Context, cmd *Command) (Process, error) {
	return e.env.Start(ctx, cmd)
}

// RunLineStream streams stdout line-by-line to onLine.
// Useful for live logging. Overrides Command.Stdout.
func (e *Executor) RunLineStream(ctx context.Context, cmd *Command, onLine func(string), _ ...ExecOption) error {
	pr, pw := io.Pipe()

	// Clone the command to avoid mutating the original
	cmdCopy := *cmd
	cmdCopy.Stdout = pw
	cmd = &cmdCopy

	// Start the process asynchronously.
	proc, err := e.Start(ctx, cmd)
	if err != nil {
		return err
	}

	defer func() { _ = proc.Close() }()

	// Process the stream in a separate goroutine to prevent blocking.
	scanErrCh := make(chan error, 1)

	go func() {
		defer func() { _ = pr.Close() }()

		scanner := bufio.NewScanner(pr)
		for scanner.Scan() {
			onLine(scanner.Text())
		}

		scanErrCh <- scanner.Err()
	}()

	// Wait for the command to finish.
	if err := proc.Wait(); err != nil {
		return err
	}

	_ = pw.Close() // Close the write end to signal the scanner to stop

	scanErr := <-scanErrCh
	if scanErr != nil {
		return fmt.Errorf("scan error: %w", scanErr)
	}

	return nil
}

// TargetOS returns the operating system of the underlying environment.
func (e *Executor) TargetOS() TargetOS {
	return e.env.TargetOS()
}

// Upload copies a local file or directory to the remote destination.
// It delegates directly to the underlying Environment.
func (e *Executor) Upload(ctx context.Context, localPath, remotePath string, opts ...FileOption) error {
	return e.env.Upload(ctx, localPath, remotePath, opts...)
}

// Download copies a remote file or directory to the local destination.
// It delegates directly to the underlying Environment.
func (e *Executor) Download(ctx context.Context, remotePath, localPath string, opts ...FileOption) error {
	return e.env.Download(ctx, remotePath, localPath, opts...)
}

func (e *Executor) applySudo(cmd *Command) *Command {
	newCmd := *cmd
	newCmd.Args = append([]string{"-n", "--", cmd.Cmd}, cmd.Args...)
	newCmd.Cmd = "sudo"

	return &newCmd
}

func (e *Executor) wait(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
