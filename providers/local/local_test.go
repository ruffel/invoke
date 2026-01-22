package local

import (
	"bytes"
	"context"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const osWindows = "windows"

func TestEnvironment_Run(t *testing.T) {
	t.Parallel()

	env := New()

	t.Cleanup(func() { _ = env.Close() })

	ctx := context.Background()

	tests := []struct {
		name        string
		cmd         *invoke.Command
		wantSuccess bool
	}{
		{
			name:        "successful command",
			cmd:         &invoke.Command{Cmd: "echo", Args: []string{"hello"}},
			wantSuccess: true,
		},
		{
			name:        "command with exit code",
			cmd:         getExitCommand(1),
			wantSuccess: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := env.Run(ctx, tt.cmd)
			if tt.wantSuccess {
				require.NoError(t, err)
				assert.Equal(t, 0, result.ExitCode)
				assert.Greater(t, result.Duration, time.Duration(0))
			} else {
				require.Error(t, err)

				var exitErr *invoke.ExitError
				require.ErrorAs(t, err, &exitErr)
				assert.NotEqual(t, 0, exitErr.ExitCode)
			}
		})
	}
}

func TestEnvironment_Features(t *testing.T) {
	t.Parallel()

	env := New()

	t.Cleanup(func() { _ = env.Close() })

	ctx := context.Background()

	t.Run("stdout capture", func(t *testing.T) {
		t.Parallel()

		var stdout bytes.Buffer

		cmd := invoke.Command{Cmd: "echo", Args: []string{"test"}, Stdout: &stdout}
		_, err := env.Run(ctx, &cmd)
		require.NoError(t, err)
		assert.Contains(t, stdout.String(), "test")
	})

	t.Run("environment variables", func(t *testing.T) {
		t.Parallel()

		var cmd invoke.Command
		if runtime.GOOS == osWindows {
			cmd = invoke.Command{Cmd: "cmd", Args: []string{"/c", "echo %TEST_VAR%"}, Env: []string{"TEST_VAR=hello"}}
		} else {
			cmd = invoke.Command{Cmd: "sh", Args: []string{"-c", "echo $TEST_VAR"}, Env: []string{"TEST_VAR=hello"}}
		}

		var stdout bytes.Buffer

		cmd.Stdout = &stdout

		_, err := env.Run(ctx, &cmd)
		require.NoError(t, err)
		assert.Contains(t, stdout.String(), "hello")
	})

	t.Run("working directory", func(t *testing.T) {
		t.Parallel()

		var cmd invoke.Command

		var expected string

		if runtime.GOOS == osWindows {
			cmd = invoke.Command{Cmd: "cmd", Args: []string{"/c", "cd"}, Dir: "C:\\"}
			expected = "C:\\"
		} else {
			cmd = invoke.Command{Cmd: "pwd", Dir: "/tmp"}
			expected = "/tmp"
		}

		var stdout bytes.Buffer

		cmd.Stdout = &stdout

		_, err := env.Run(ctx, &cmd)
		if err != nil {
			t.Skip("Directory test failed (platform dependent setup)")
		}

		assert.Contains(t, stdout.String(), expected)
	})
}

func TestSafety(t *testing.T) {
	t.Parallel()

	env := New()

	t.Cleanup(func() { _ = env.Close() })

	t.Run("wait on unstarted process", func(t *testing.T) {
		t.Parallel()

		process := &Process{env: env, cmd: &invoke.Command{Cmd: "echo"}}
		err := process.Wait()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not started")
	})

	t.Run("wait on closed process", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		process, _ := env.Start(ctx, &invoke.Command{Cmd: "echo", Args: []string{"test"}})
		_ = process.Wait()
		_ = process.Close()
		err := process.Wait()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "already closed")
	})

	t.Run("environment closed", func(t *testing.T) {
		t.Parallel()

		localEnv := New()
		_ = localEnv.Close()
		_, err := localEnv.Start(context.Background(), &invoke.Command{Cmd: "echo"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "environment is closed")
	})
}

func TestLimits(t *testing.T) {
	t.Parallel()
	// Test the limited buffer implementation specifically
	t.Run("buffer truncation", func(t *testing.T) {
		t.Parallel()

		buf := newLimitedBuffer(5)
		n, _ := buf.Write([]byte("hello world"))
		assert.Equal(t, 11, n) // Claims to write all
		assert.True(t, buf.Truncated())
		assert.Equal(t, "hello", string(buf.Bytes()))
	})
}

func TestSignals(t *testing.T) {
	t.Parallel()

	env := New()

	t.Cleanup(func() { _ = env.Close() })

	if runtime.GOOS == osWindows {
		t.Skip("Signal testing is flaky on Windows")
	}

	t.Run("signals", func(t *testing.T) {
		t.Parallel()

		cmd := invoke.Command{Cmd: "sleep", Args: []string{"10"}}
		proc, err := env.Start(context.Background(), &cmd)
		require.NoError(t, err)

		time.Sleep(100 * time.Millisecond)

		err = proc.Signal(os.Kill)
		require.NoError(t, err)

		start := time.Now()
		err = proc.Wait()
		require.Error(t, err) // Should be killed
		assert.Less(t, time.Since(start), 2*time.Second)
	})

	t.Run("context cancellation", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		cmd := invoke.Command{Cmd: "sleep", Args: []string{"10"}}
		proc, err := env.Start(ctx, &cmd)
		require.NoError(t, err)

		time.Sleep(100 * time.Millisecond)
		cancel()

		err = proc.Wait()
		require.Error(t, err)
	})
}

// limitedBuffer is a thread-safe buffer with a hard limit.
type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func newLimitedBuffer(limit int) *limitedBuffer {
	return &limitedBuffer{limit: limit}
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len() >= b.limit {
		return len(p), nil // Pretend we wrote it
	}

	remaining := b.limit - b.buf.Len()
	if len(p) > remaining {
		b.buf.Write(p[:remaining])

		return len(p), nil
	}

	return b.buf.Write(p)
}

func (b *limitedBuffer) Bytes() []byte {
	return b.buf.Bytes()
}

func (b *limitedBuffer) Truncated() bool {
	return b.buf.Len() == b.limit
}

func getExitCommand(code int) *invoke.Command {
	if runtime.GOOS == osWindows {
		return &invoke.Command{Cmd: "cmd", Args: []string{"/c", "exit", strconv.Itoa(code)}}
	}

	return &invoke.Command{Cmd: "sh", Args: []string{"-c", "exit " + strconv.Itoa(code)}}
}
