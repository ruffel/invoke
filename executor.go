package invoke

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/term"
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

	// Apply sudo if requested
	cmd = e.applySudo(cfg, cmd)

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

		// Check if it's an ExitError - this is a terminal result, don't retry.
		var exitErr *ExitError
		if errors.As(lastErr, &exitErr) {
			return lastRes, lastErr
		}
	}

	if lastErr != nil {
		return lastRes, &TransportError{
			Command: cmd,
			Err:     lastErr,
		}
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

	// If we got an ExitError, ensure Stderr is attached for context.
	if err != nil {
		var exitErr *ExitError
		if errors.As(err, &exitErr) {
			if len(exitErr.Stderr) == 0 {
				exitErr.Stderr = stderrBuf.Bytes()
			}
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
	waitErr := proc.Wait()

	_ = pw.Close() // Close the write end to signal the scanner to stop

	scanErr := <-scanErrCh

	if waitErr != nil {
		return waitErr
	}

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

// RunInteractiveTTY executes a command in interactive TTY mode.
//
// If Stdin/Stdout/Stderr are nil they default to os.Stdin/os.Stdout/os.Stderr.
// This behavior is specific to RunInteractiveTTY and overrides the default
// Command behavior, where nil streams typically default to empty/discard.
// When reading from the current terminal, stdin is switched to raw mode for the
// duration of the command and restored afterwards.
func (e *Executor) RunInteractiveTTY(ctx context.Context, cmd *Command, opts ...ExecOption) (*Result, error) {
	if cmd == nil {
		return e.Run(ctx, nil, opts...)
	}

	cmdCopy := *cmd
	cmdCopy.Tty = true

	if cmdCopy.Stdin == nil {
		cmdCopy.Stdin = os.Stdin
	}

	if cmdCopy.Stdout == nil {
		cmdCopy.Stdout = os.Stdout
	}

	if cmdCopy.Stderr == nil {
		cmdCopy.Stderr = os.Stderr
	}

	stdinFile, ok := cmdCopy.Stdin.(*os.File)
	if ok && stdinFile == os.Stdin {
		fd := int(os.Stdin.Fd())
		if term.IsTerminal(fd) {
			state, err := term.MakeRaw(fd)
			if err != nil {
				return nil, fmt.Errorf("failed to set terminal raw mode: %w", err)
			}

			defer func() {
				_ = term.Restore(fd, state)
			}()
		}
	}

	return e.Run(ctx, &cmdCopy, opts...)
}

func (e *Executor) applySudo(cfg ExecConfig, cmd *Command) *Command {
	if cfg.SudoConfig == nil {
		return cmd
	}

	sudoArgs := []string{"-n"}

	if cfg.SudoConfig.User != "" {
		sudoArgs = append(sudoArgs, "-u", cfg.SudoConfig.User)
	}

	if cfg.SudoConfig.Group != "" {
		sudoArgs = append(sudoArgs, "-g", cfg.SudoConfig.Group)
	}

	if cfg.SudoConfig.PreserveEnv {
		sudoArgs = append(sudoArgs, "-E")
	}

	sudoArgs = append(sudoArgs, cfg.SudoConfig.CustomFlags...)
	sudoArgs = append(sudoArgs, "--", cmd.Cmd)

	newCmd := *cmd
	newCmd.Cmd = "sudo"

	// Combine args (pre-allocate for performance and linting)
	args := make([]string, 0, len(sudoArgs)+len(cmd.Args))
	args = append(args, sudoArgs...)
	args = append(args, cmd.Args...)
	newCmd.Args = args

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
