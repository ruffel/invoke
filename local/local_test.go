package local_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/local"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newEnv constructs an Environment and ties its lifetime to the test.
func newEnv(t *testing.T) *local.Environment {
	t.Helper()

	env, err := local.New()
	require.NoError(t, err, "local.New()")

	t.Cleanup(func() { _ = env.Close() })

	return env
}

func TestNew(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	assert.Equal(t, runtime.GOOS, string(env.OS()), "OS()")

	caps := env.Capabilities()
	assert.False(t, caps.TTY, "Capabilities().TTY = true; local does not implement PTY allocation this cycle")
	assert.True(t, caps.Signals && caps.SymlinkPreserve,
		"Capabilities() = %+v, want Signals and SymlinkPreserve declared", caps)
}

func TestCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	require.NoError(t, env.Close(), "first Close()")
	require.NoError(t, env.Close(), "second Close()")
}

func TestClosedEnvironmentRefusesEverything(t *testing.T) {
	t.Parallel()

	env := newEnv(t)
	_ = env.Close()

	ctx := t.Context()

	_, err := env.Start(ctx, invoke.New("true"), invoke.IO{})
	assert.ErrorIs(t, err, invoke.ErrClosed, "Start after Close")

	_, err = env.LookPath(ctx, "sh")
	assert.ErrorIs(t, err, invoke.ErrClosed, "LookPath after Close")

	assert.ErrorIs(t, env.Upload(ctx, "a", "b"), invoke.ErrClosed, "Upload after Close")
	assert.ErrorIs(t, env.Download(ctx, "a", "b"), invoke.ErrClosed, "Download after Close")
}

func TestLookPath(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	path, err := env.LookPath(t.Context(), "sh")
	require.NoError(t, err, "LookPath(sh)")
	assert.True(t, filepath.IsAbs(path), "LookPath(sh) = %q, want an absolute path", path)

	_, err = env.LookPath(t.Context(), "definitely-not-a-real-binary-abc123")
	assert.ErrorIs(t, err, invoke.ErrNotFound, "LookPath(missing)")
}
