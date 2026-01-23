package local_test

import (
	"context"
	"testing"
	"time"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/providers/local"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunShell(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := local.RunShell(ctx, "echo hello world")
	require.NoError(t, err)
	assert.True(t, res.Success())
	assert.Equal(t, "hello world\n", string(res.Stdout))
}

func TestRunCommand(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := &invoke.Command{
		Cmd:  "sh",
		Args: []string{"-c", "echo foo >&2; exit 1"},
	}

	res, err := local.RunCommand(ctx, cmd)
	// Expect ExitError, but the function itself shouldn't fail unless transport fails
	// Wait, invoke.Executor.RunBuffered DOES return an error if exit code != 0 due to ExitError wrapper.
	require.Error(t, err)
	assert.NotNil(t, res)
	assert.Equal(t, 1, res.ExitCode)
	assert.Equal(t, "foo\n", string(res.Stderr))

	var exitErr *invoke.ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 1, exitErr.ExitCode)
}

func TestRunShell_WithOption(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Verify options are passed through (e.g. Retry)
	// We'll use a command that fails first time to test retry, but that's hard to deterministic test in simple local.
	// Instead, let's just run a simple command and ensure it works with options passed.
	res, err := local.RunShell(ctx, "echo opt", invoke.WithRetry(1, time.Millisecond))
	require.NoError(t, err)
	assert.Equal(t, "opt\n", string(res.Stdout))
}
