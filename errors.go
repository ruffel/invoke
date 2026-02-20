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

// ExitError represents a successful execution that resulted in a non-zero exit code.
type ExitError struct {
	Command  *Command
	ExitCode int
	Stderr   []byte
	Cause    error
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

// TransportError represents a failure in the underlying transport or provider layer
// (e.g. connection lost, docker daemon unreachable, binary not found).
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
