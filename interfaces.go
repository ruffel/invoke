// Package invoke provides a unified interface for command execution and file transfer
// across Local, SSH, and Docker environments.
//
// # Core Interfaces
//
//   - [Environment]: The connection to a system. Swap the provider to switch targets.
//   - [Process]: A handle to a running command (Wait, Signal, Close).
//
// # Streaming
//
// invoke is streaming-first. Output is not buffered by default — attach an [io.Writer]
// to your [Command]. For convenience, use [Executor.RunBuffered] to capture output.
//
// # Error Model
//
//   - [ExitError]: The command ran but exited non-zero. Contains exit code and stderr.
//   - [TransportError]: The underlying transport failed (connection lost, daemon unreachable).
//     Retryable via [WithRetry].
//
// # Privilege Escalation
//
// Supported via [WithSudo]. Uses sudo -n for non-interactive execution.
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
	// Returns the result (exit code, duration) and any error.
	// Output is not captured by default; attach writers to Command.Stdout/Stderr.
	Run(ctx context.Context, cmd *Command) (*Result, error)

	// Start initiates a command asynchronously.
	// The caller manages the returned Process (Wait/Signal) and must ensure resources
	// are released via Wait() or Close().
	Start(ctx context.Context, cmd *Command) (Process, error)

	// TargetOS returns the operating system of the target environment.
	TargetOS() TargetOS

	// Upload copies a local file or directory to the remote destination.
	// Missing parent directories at the destination are created automatically.
	Upload(ctx context.Context, localPath, remotePath string, opts ...FileOption) error

	// Download copies a remote file or directory to the local destination.
	// Missing parent directories at the local destination are created automatically.
	Download(ctx context.Context, remotePath, localPath string, opts ...FileOption) error

	// LookPath searches for an executable named file in the PATH of the target environment.
	LookPath(ctx context.Context, file string) (string, error)
}

// Process represents a command that has been started but not yet completed.
type Process interface {
	io.Closer

	// Wait blocks until the process exits and returns the result.
	//
	// On success (exit code 0), returns (*Result, nil).
	// On non-zero exit, returns (*Result, *ExitError) — the Result is always
	// populated with the exit code and duration, even when an error is returned.
	//
	// Wait is idempotent: calling it multiple times returns the same result.
	Wait() (*Result, error)

	// Signal sends an OS signal to the process.
	// Support for specific signals depends on the underlying provider.
	Signal(sig os.Signal) error
}
