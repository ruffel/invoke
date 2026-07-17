package invoke

import (
	"context"
	"io"
)

// Environment is a connection to an execution target: the local machine, a
// remote host, a container. Implementations are provided by subpackages;
// all of them satisfy the same behavioral contracts (verified by the
// invoketest suite), so swapping targets does not change semantics.
//
// Lifecycle: Close is idempotent. After Close, every method fails with an
// error wrapping [ErrClosed], and processes still running are terminated.
type Environment interface {
	io.Closer

	// Start launches cmd on the target with stdio wiring its streams,
	// and returns a handle to the running process.
	//
	// The context owns the process: when ctx is canceled the provider
	// terminates the process promptly, and Wait returns an error
	// matching ctx.Err().
	Start(ctx context.Context, cmd Command, stdio IO) (Process, error)

	// LookPath resolves name to an executable path on the target,
	// searching the target's own lookup path. If the name cannot be
	// resolved, the error wraps [ErrNotFound].
	LookPath(ctx context.Context, name string) (string, error)

	// Upload copies a local file or directory tree to the target.
	// Writes are atomic per file (a failed transfer never corrupts an
	// existing destination), and missing parent directories at the
	// destination are created.
	Upload(ctx context.Context, localPath, remotePath string, opts ...TransferOption) error

	// Download copies a file or directory tree from the target to the
	// local filesystem, with the same atomicity and parent-creation
	// semantics as Upload.
	Download(ctx context.Context, remotePath, localPath string, opts ...TransferOption) error

	// OS reports the target's operating system.
	OS() TargetOS

	// Capabilities reports which optional features this target supports.
	// A declared capability works; an undeclared one fails with an error
	// wrapping [ErrNotSupported]. There is no silent middle ground.
	Capabilities() Capabilities
}

// Process is a handle to a command started by [Environment.Start].
type Process interface {
	// Wait blocks until the process exits and returns its Result. It is
	// idempotent: repeated calls return the same outcome without
	// blocking again.
	//
	// The (Result, error) pair follows the package error taxonomy: nil
	// error means exit zero; an [ExitError] means the process ran and
	// terminated unsuccessfully; any other error means the process did
	// not run to completion. Cancellation and Close surface as errors
	// matching ctx.Err() or wrapping [ErrClosed] — never as ExitError.
	Wait() (Result, error)

	// Signal delivers sig to the process. It either delivers the signal
	// or returns an error (wrapping [ErrNotSupported] when the target
	// cannot deliver it); it never silently does nothing.
	Signal(sig Signal) error

	// Close releases the handle, terminating the process if it is still
	// running. It is idempotent, and it unblocks any Wait in progress —
	// which then reports an error wrapping [ErrClosed], never an
	// ExitError, for a process killed by Close.
	io.Closer
}

// Capabilities declares the optional features an [Environment] supports.
//
// The contract is symmetric: a true field means the feature demonstrably
// works on this target; a false field means using it fails with an error
// wrapping [ErrNotSupported]. Providers never advertise a capability they
// cannot deliver, and never quietly ignore a request for one they lack.
type Capabilities struct {
	// TTY reports whether the target can allocate a pseudo-terminal for
	// a command (IO.TTY).
	TTY bool

	// Signals reports whether Process.Signal actually delivers signals
	// to the running process.
	Signals bool

	// SymlinkPreserve reports whether file transfers can recreate
	// symbolic links as links ([SymlinkPreserve]).
	SymlinkPreserve bool
}
