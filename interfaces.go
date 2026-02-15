// Package invoke provides a unified interface for command execution and file transfer.
//
// # Core Interfaces
//
// - Environment: The connection to a system (Local, SSH, Docker).
// - Process: A running command handle (allows Wait, Signal, Close).
//
// # Streaming
//
// `invoke` is streaming-first. We don't buffer output by default. If you want to capture stdout/stderr, attach an `io.Writer` to your `Command`.
//
// For simple "just give me the output" cases, use the `Executor` wrapper.
//
// # Sudo
//
// Privilege escalation is supported via `invoke.WithSudo()`. This uses `sudo -n` for non-interactive execution.
package invoke

import (
	"context"
	"io"
	"os"
)

// Environment abstracts the underlying system where commands are executed (e.g., Local, SSH, Docker).
type Environment interface {
	io.Closer

	// Run executes a command synchronously.
	// Returns the result (exit code, error). Output is not captured by default; use Command.Stdout/Stderr.
	Run(ctx context.Context, cmd *Command) (*Result, error)

	// Start initiates a command asynchronously.
	// The caller manages the returned Process (Wait/Signal) and must ensure resources are released via
	// either Wait() or Close().
	Start(ctx context.Context, cmd *Command) (Process, error)

	// TargetOS returns the operating system of the target environment.
	TargetOS() TargetOS

	// Upload copies a local file or directory to the remote destination.
	//
	// It creates any missing parent directories at the destination.
	Upload(ctx context.Context, localPath, remotePath string, opts ...FileOption) error

	// Download copies a remote file or directory to the local destination.
	//
	// It creates any missing parent directories at the local destination.
	Download(ctx context.Context, remotePath, localPath string, opts ...FileOption) error

	// LookPath searches for an executable named file in the directories named by
	// the PATH environment variable.
	LookPath(ctx context.Context, file string) (string, error)
}

// Process represents a command that has been started but not yet completed.
type Process interface {
	io.Closer

	// Wait blocks until the process exits.
	// Returns an error if the exit code is non-zero.
	Wait() error

	// Result returns metadata (exit code, termination status) (only valid after Wait).
	Result() *Result

	// Signal sends an OS signal to the process.
	// Note: support for specific signals depends on the underlying provider.
	Signal(sig os.Signal) error
}
