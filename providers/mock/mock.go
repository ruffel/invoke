package mock

import (
	"context"
	"io"
	"os"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/mock"
)

// Environment implements a mock invoke.Environment using testify/mock.
type Environment struct {
	mock.Mock
}

var _ invoke.Environment = (*Environment)(nil)

// New creates a new mock environment.
func New() *Environment {
	return &Environment{}
}

// Upload mocks uploading a file to the remote environment.
func (m *Environment) Upload(ctx context.Context, localPath, remotePath string, opts ...invoke.FileOption) error {
	// Variadic capture fix for testify
	args := m.Called(ctx, localPath, remotePath, opts)

	return args.Error(0)
}

// Download mocks downloading a file from the remote environment.
func (m *Environment) Download(ctx context.Context, remotePath, localPath string, opts ...invoke.FileOption) error {
	args := m.Called(ctx, remotePath, localPath, opts)

	return args.Error(0)
}

// Run mocks running a command to completion.
func (m *Environment) Run(ctx context.Context, cmd *invoke.Command) (*invoke.Result, error) {
	args := m.Called(ctx, cmd)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}

	return args.Get(0).(*invoke.Result), args.Error(1)
}

// Start mocks starting a command asynchronously.
func (m *Environment) Start(ctx context.Context, cmd *invoke.Command) (invoke.Process, error) {
	args := m.Called(ctx, cmd)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}

	return args.Get(0).(invoke.Process), args.Error(1)
}

// TargetOS mocks returning the target operating system.
func (m *Environment) TargetOS() invoke.TargetOS {
	args := m.Called()

	return args.Get(0).(invoke.TargetOS)
}

// Close mocks closing the environment.
func (m *Environment) Close() error {
	args := m.Called()

	return args.Error(0)
}

// Process implements a mock invoke.Process using testify/mock.
type Process struct {
	mock.Mock
}

var _ invoke.Process = (*Process)(nil)

// Wait mocks waiting for the process to complete.
func (m *Process) Wait() error {
	args := m.Called()

	return args.Error(0)
}

// Result mocks returning the process result.
func (m *Process) Result() *invoke.Result {
	args := m.Called()
	if args.Get(0) == nil {
		return nil
	}

	return args.Get(0).(*invoke.Result)
}

// Signal mocks sending a signal to the process.
func (m *Process) Signal(sig os.Signal) error {
	args := m.Called(sig)

	return args.Error(0)
}

// Close mocks closing the process.
func (m *Process) Close() error {
	args := m.Called()

	return args.Error(0)
}

// WriteOutput is a helper to simulate output writing for mocked processes.
// Usage: mockProcess.On("Wait").Run(WriteOutput(cmd.Stdout, "output")).Return(nil).
func WriteOutput(w io.Writer, content string) func(mock.Arguments) {
	return func(args mock.Arguments) {
		if w != nil {
			_, _ = io.WriteString(w, content)
		}
	}
}
