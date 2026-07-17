package invoke

import (
	"errors"
	"fmt"
)

// The error taxonomy distinguishes three families of failure, and every
// provider classifies into it:
//
//   - The command ran and failed: [ExitError]. Terminal — never retried,
//     because the command may have had side effects.
//   - The command could not run because the transport failed (connection
//     lost, daemon unreachable): [TransportError]. The only retryable
//     family.
//   - The command could not run for a deterministic reason, or the caller
//     stopped it: errors wrapping the sentinels below, or the context's
//     own error. Terminal.
//
// Anything unclassified is treated as terminal: retrying is the behavior
// that must be earned by explicit classification, not the default.
var (
	// ErrClosed reports an operation on an environment or process that
	// has been closed.
	ErrClosed = errors.New("invoke: closed")

	// ErrNotSupported reports a feature the target cannot provide, such
	// as TTY allocation or signal delivery. Providers return it (wrapped,
	// with context) rather than silently ignoring the request.
	ErrNotSupported = errors.New("invoke: not supported")

	// ErrNotFound reports that the executable could not be resolved on
	// the target.
	ErrNotFound = errors.New("invoke: executable not found")

	// ErrInvalidWorkdir reports that the requested working directory does
	// not exist or cannot be entered on the target.
	ErrInvalidWorkdir = errors.New("invoke: invalid working directory")
)

// ExitError reports that a command ran and terminated unsuccessfully —
// either by exiting non-zero or by being killed by a signal.
//
// It is always terminal: the command executed, so retrying is never safe
// to assume. Extract it with errors.As:
//
//	var exitErr *invoke.ExitError
//	if errors.As(err, &exitErr) {
//	    log.Printf("exited %d", exitErr.Code)
//	}
type ExitError struct {
	// Code is the exit status. It is -1 when the process was terminated
	// by a signal.
	Code int

	// Signal is set when the process was terminated by a signal instead
	// of exiting.
	Signal Signal

	// Stderr optionally holds a tail of the process's standard error,
	// attached by capture helpers for diagnostics. Providers leave it
	// empty when output went to the caller's own writers.
	Stderr []byte
}

// Error returns a description of how the process terminated.
func (e *ExitError) Error() string {
	if e.Signal != "" {
		return fmt.Sprintf("process terminated by signal %s", e.Signal)
	}

	return fmt.Sprintf("process exited with code %d", e.Code)
}

// TransportError reports that the transport to the target failed before or
// while the command ran: a lost connection, an unreachable daemon, a broken
// session.
//
// It is the only retryable family in the taxonomy: the failure is
// environmental, not a verdict about the command.
type TransportError struct {
	// Op names the operation that failed: "start", "wait", "upload",
	// "download", "lookpath".
	Op string

	// Err is the underlying transport failure.
	Err error
}

// Error describes the failed operation and its cause.
func (e *TransportError) Error() string {
	return fmt.Sprintf("transport failure during %s: %v", e.Op, e.Err)
}

// Unwrap returns the underlying transport failure.
func (e *TransportError) Unwrap() error {
	return e.Err
}
