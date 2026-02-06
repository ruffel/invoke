package invoke

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// MockEnv is a simple mock for testing Executor.
type MockEnv struct {
	mock.Mock
}

func (m *MockEnv) Run(ctx context.Context, cmd *Command) (*Result, error) {
	args := m.Called(ctx, cmd)
	if r := args.Get(0); r != nil {
		return r.(*Result), args.Error(1)
	}

	return nil, args.Error(1)
}

func (m *MockEnv) Start(ctx context.Context, cmd *Command) (Process, error) {
	args := m.Called(ctx, cmd)
	if p := args.Get(0); p != nil {
		return p.(Process), args.Error(1)
	}

	return nil, args.Error(1)
}

func (m *MockEnv) Close() error {
	return m.Called().Error(0)
}

func (m *MockEnv) Upload(ctx context.Context, src, dst string, opts ...FileOption) error {
	return m.Called(ctx, src, dst, opts).Error(0)
}

func (m *MockEnv) Download(ctx context.Context, src, dst string, opts ...FileOption) error {
	return m.Called(ctx, src, dst, opts).Error(0)
}

func (m *MockEnv) TargetOS() TargetOS {
	return OSLinux
}

func (m *MockEnv) LookPath(_ context.Context, file string) (string, error) {
	args := m.Called(file)

	return args.String(0), args.Error(1)
}

// MockProcess is a mock for invoke.Process.
type MockProcess struct {
	mock.Mock
}

func (m *MockProcess) Wait() error {
	return m.Called().Error(0)
}

func (m *MockProcess) Result() *Result {
	args := m.Called()
	if r := args.Get(0); r != nil {
		return r.(*Result)
	}

	return nil
}

func (m *MockProcess) Signal(sig os.Signal) error {
	return m.Called(sig).Error(0)
}

func (m *MockProcess) Close() error {
	return m.Called().Error(0)
}

func TestExecutor_LookPath(t *testing.T) {
	t.Parallel()

	mockEnv := new(MockEnv)
	exec := NewExecutor(mockEnv)

	// Success case: Environment finds the path
	mockEnv.On("LookPath", "docker").Return("/usr/bin/docker", nil)

	path, err := exec.LookPath(context.Background(), "docker")
	require.NoError(t, err)
	assert.Equal(t, "/usr/bin/docker", path)

	// Failure case: Environment returns error
	mockEnv.On("LookPath", "missing").Return("", errors.New("exec: executable file not found in $PATH"))

	_, err = exec.LookPath(context.Background(), "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exec: executable file not found")
}

func TestExecutor_RunShell(t *testing.T) {
	t.Parallel()

	mockEnv := new(MockEnv)
	exec := NewExecutor(mockEnv)

	mockEnv.On("Run", mock.Anything, mock.MatchedBy(func(c *Command) bool {
		return c.Cmd == "sh" && len(c.Args) == 2
	})).Return(&Result{ExitCode: 0}, nil)

	_, err := exec.RunShell(context.Background(), "echo hello")
	assert.NoError(t, err)
}

func TestExecutor_Start(t *testing.T) {
	t.Parallel()

	mockEnv := new(MockEnv)
	exec := NewExecutor(mockEnv)

	mockProc := new(MockProcess)

	mockEnv.On("Start", mock.Anything, mock.MatchedBy(func(c *Command) bool {
		return c.Cmd == "sleep"
	})).Return(mockProc, nil)

	_, err := exec.Start(context.Background(), &Command{Cmd: "sleep"})
	assert.NoError(t, err)
}

func TestExecutor_RunLineStream(t *testing.T) {
	t.Parallel()

	mockEnv := new(MockEnv)
	exec := NewExecutor(mockEnv)
	mockProc := new(MockProcess) // Create a mock process

	// Use a channel to synchronize write completion
	writeDone := make(chan struct{})

	mockEnv.On("Start", mock.Anything, mock.MatchedBy(func(c *Command) bool {
		return c.Cmd == "stream" && c.Stdout != nil
	})).Run(func(args mock.Arguments) {
		cmd := args.Get(1).(*Command)
		// Write some data to the pipe asynchronously
		go func() {
			if w, ok := cmd.Stdout.(io.WriteCloser); ok {
				_, _ = w.Write([]byte("line1\nline2\n"))
				_ = w.Close()
			}

			close(writeDone) // Signal done
		}()
	}).Return(mockProc, nil)

	// Make Wait block until writes are done
	mockProc.On("Wait").Run(func(_ mock.Arguments) {
		<-writeDone
	}).Return(nil)
	mockProc.On("Close").Return(nil)

	var lines []string

	err := exec.RunLineStream(context.Background(), &Command{Cmd: "stream"}, func(line string) {
		lines = append(lines, line)
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"line1", "line2"}, lines)
}

func TestExecutor_Run_Retry(t *testing.T) {
	t.Parallel()

	mockEnv := new(MockEnv)
	exec := NewExecutor(mockEnv)

	// We expect the command to fail twice, then succeed on the third try
	cmd := &Command{Cmd: "flaky"}

	// Failure 1
	mockEnv.On("Run", mock.Anything, cmd).Return(&Result{ExitCode: 1}, nil).Once()
	// Failure 2
	mockEnv.On("Run", mock.Anything, cmd).Return(nil, errors.New("transport error")).Once()
	// Success
	mockEnv.On("Run", mock.Anything, cmd).Return(&Result{ExitCode: 0}, nil).Once()

	// Execute with 3 attempts and a tiny delay
	res, err := exec.Run(context.Background(), cmd, WithRetry(3, time.Millisecond))

	require.NoError(t, err)
	assert.NotNil(t, res)
	assert.Equal(t, 0, res.ExitCode)

	mockEnv.AssertExpectations(t)
}

func TestExecutor_Run_RetryFail(t *testing.T) {
	t.Parallel()

	mockEnv := new(MockEnv)
	exec := NewExecutor(mockEnv)

	cmd := &Command{Cmd: "always_fail"}

	// Should run 2 times and fail both
	mockEnv.On("Run", mock.Anything, cmd).Return(&Result{ExitCode: 1}, nil).Times(2)

	// Execute with 2 attempts
	res, err := exec.Run(context.Background(), cmd, WithRetry(2, time.Millisecond))

	// You changed Executor.Run to return an error on non-zero exit codes.
	// So we expect an error now.
	require.Error(t, err)

	var exitErr *ExitError
	if assert.ErrorAs(t, err, &exitErr) {
		assert.Equal(t, 1, exitErr.ExitCode)
	}

	assert.NotNil(t, res)
	assert.Equal(t, 1, res.ExitCode)

	mockEnv.AssertExpectations(t)
}

func TestExecutor_FileTransfer(t *testing.T) {
	t.Parallel()

	mockEnv := new(MockEnv)
	exec := NewExecutor(mockEnv)

	// Test Upload
	mockEnv.On("Upload", mock.Anything, "local", "remote", mock.Anything).Return(nil)

	err := exec.Upload(context.Background(), "local", "remote")
	require.NoError(t, err)

	// Test Download
	mockEnv.On("Download", mock.Anything, "remote", "local", mock.Anything).Return(nil)

	err = exec.Download(context.Background(), "remote", "local")
	require.NoError(t, err)

	mockEnv.AssertExpectations(t)
}

const sudoCmd = "sudo"

func TestExecutor_SudoLegacy(t *testing.T) {
	t.Parallel()

	mockEnv := new(MockEnv)
	exec := NewExecutor(mockEnv)

	cmd := &Command{Cmd: "ls", Args: []string{"-la"}}

	mockEnv.On("Run", mock.Anything, mock.MatchedBy(func(c *Command) bool {
		// Expect: sudo -n -- ls -la
		return c.Cmd == sudoCmd &&
			len(c.Args) == 4 && // -n, --, ls, -la
			c.Args[0] == "-n" &&
			c.Args[1] == "--" &&
			c.Args[2] == "ls" &&
			c.Args[3] == "-la"
	})).Return(&Result{ExitCode: 0}, nil)

	_, err := exec.Run(context.Background(), cmd, WithSudo())
	require.NoError(t, err)

	mockEnv.AssertExpectations(t)
}

func TestExecutor_SudoConfig(t *testing.T) {
	t.Parallel()

	mockEnv := new(MockEnv)
	exec := NewExecutor(mockEnv)

	cmd := &Command{Cmd: "ps", Args: []string{"aux"}}

	mockEnv.On("Run", mock.Anything, mock.MatchedBy(func(c *Command) bool {
		// Expect: sudo -n -u postgres -g admin -E --custom -- ps aux
		// Args index:
		// 0: -n
		// 1: -u
		// 2: postgres
		// 3: -g
		// 4: admin
		// 5: -E
		// 6: --custom
		// 7: --
		// 8: ps
		// 9: aux
		if c.Cmd != sudoCmd {
			return false
		}
		// Basic checks to ensure flags are present in some order (though Slice order is deterministic in implementation)
		// We can check exact slice match
		expected := []string{"-n", "-u", "postgres", "-g", "admin", "-E", "--custom", "--", "ps", "aux"}

		return assert.ObjectsAreEqual(expected, c.Args)
	})).Return(&Result{ExitCode: 0}, nil)

	_, err := exec.Run(context.Background(), cmd, WithSudo(
		WithSudoUser("postgres"),
		func(c *SudoConfig) { c.Group = "admin" }, // Manual functional option
		WithSudoPreserveEnv(),
		func(c *SudoConfig) { c.CustomFlags = []string{"--custom"} },
	))
	require.NoError(t, err)

	mockEnv.AssertExpectations(t)
}
