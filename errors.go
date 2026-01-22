package invoke

import (
	"errors"
	"fmt"
)

// ErrNotSupported indicates that the requested feature (e.g., TTY) is not supported
// by the specific provider or OS.
var ErrNotSupported = errors.New("operation not supported")

// ErrEnvironmentClosed indicates that an operation was attempted on a closed environment.
var ErrEnvironmentClosed = errors.New("environment is closed")

// ExitError represents a command that ran but exited with a non-zero code.
//
// When Wait() returns an ExitError, the accompanying *Result is still populated
// with the exit code and duration. Use errors.As to extract the exit code:
//
//	var exitErr *invoke.ExitError
//	if errors.As(err, &exitErr) {
//	    fmt.Println("exit code:", exitErr.ExitCode)
//	}
type ExitError struct {
	Command  *Command
	ExitCode int
	Stderr   []byte // Populated by RunBuffered for convenience
	Cause    error  // Underlying error from the OS/transport layer
}

func (e *ExitError) Error() string {
	if e.Command == nil {
		return fmt.Sprintf("command exited with code %d", e.ExitCode)
	}

	return fmt.Sprintf("command %q exited with code %d", e.Command.String(), e.ExitCode)
}

func (e *ExitError) Unwrap() error {
	return e.Cause
}

// TransportError represents a failure in the underlying transport layer
// (e.g., connection lost, docker daemon unreachable, binary not found).
//
// TransportErrors are retryable via WithRetry, unlike ExitErrors which
// represent a definitive command result.
type TransportError struct {
	Command *Command
	Err     error
}

func (e *TransportError) Error() string {
	if e.Command == nil {
		return fmt.Sprintf("transport error: %v", e.Err)
	}

	return fmt.Sprintf("transport error executing %q: %v", e.Command.String(), e.Err)
}

func (e *TransportError) Unwrap() error {
	return e.Err
}
