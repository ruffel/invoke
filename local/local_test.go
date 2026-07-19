package local_test

import (
	"io"
	"path/filepath"
	"runtime"
	"testing"
	"time"

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
	assert.True(t, caps.TTY && caps.Signals && caps.SymlinkPreserve,
		"Capabilities() = %+v, want every capability declared", caps)
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

// TestTerminationGraceBoundsTheWait checks the configured grace period is
// what Wait actually observes.
//
// A command can exit while something it left behind still holds its
// output open. Waiting for that forever would hang the caller, so Wait
// gives up after the grace period — and the point of configuring it is
// that the caller chooses when, which is only true if the value reaches
// os/exec.
func TestTerminationGraceBoundsTheWait(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		grace   time.Duration
		atLeast time.Duration
		atMost  time.Duration
	}{
		"short grace gives up sooner": {grace: 250 * time.Millisecond, atMost: time.Second},
		"longer grace waits longer":   {grace: 2 * time.Second, atLeast: time.Second, atMost: 5 * time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			env, err := local.New(local.WithTerminationGrace(tc.grace))
			require.NoError(t, err)

			t.Cleanup(func() { _ = env.Close() })

			// The background sleep inherits the output pipe and outlives
			// the shell, so what bounds Wait is the grace period alone.
			proc, err := env.Start(t.Context(),
				invoke.Shell("sleep 30 & echo started"), invoke.IO{Stdout: io.Discard})
			require.NoError(t, err)

			begun := time.Now()

			_, _ = proc.Wait()

			elapsed := time.Since(begun)

			assert.Less(t, elapsed, tc.atMost, "Wait outlasted the configured grace period")

			if tc.atLeast > 0 {
				assert.Greater(t, elapsed, tc.atLeast, "Wait gave up before the configured grace period")
			}
		})
	}
}
