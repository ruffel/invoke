package local_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/local"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newLocalEnv builds a fresh environment and closes it with the test.
func newLocalEnv(t *testing.T) *local.Environment {
	t.Helper()

	env, err := local.New()
	require.NoError(t, err)

	t.Cleanup(func() { _ = env.Close() })

	return env
}

// TestUnenterableWorkdirIsClassified pins the second half of
// ErrInvalidWorkdir's promise: a directory that "does not exist or
// cannot be entered". A mode-000 directory exists and stats cleanly;
// only an enterability check keeps it from surfacing as an exec failure
// that blames the binary.
func TestUnenterableWorkdirIsClassified(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root enters everything; the classification is unobservable")
	}

	dir := filepath.Join(t.TempDir(), "sealed")
	require.NoError(t, os.Mkdir(dir, 0o755))
	require.NoError(t, os.Chmod(dir, 0))

	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	env := newLocalEnv(t)

	cmd := invoke.New("true")
	cmd.Dir = dir

	_, err := env.Start(t.Context(), cmd, invoke.IO{})
	require.Error(t, err, "a sealed workdir cannot be used")

	assert.ErrorIs(t, err, invoke.ErrInvalidWorkdir,
		"a directory that cannot be entered is the workdir's fault")
	assert.NotErrorIs(t, err, invoke.ErrNotFound,
		"the binary exists; the workdir must not be blamed on it")
}

// TestUnexecutableBinaryIsNotFound pins Start and LookPath to the same
// reading of the same input: a file without its execute bit did not
// resolve to something runnable, whichever door it came through.
func TestUnexecutableBinaryIsNotFound(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "not-runnable")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"), 0o644))

	env := newLocalEnv(t)

	_, lookErr := env.LookPath(t.Context(), path)
	assert.ErrorIs(t, lookErr, invoke.ErrNotFound, "LookPath's reading")

	proc, startErr := env.Start(t.Context(), invoke.New(path), invoke.IO{})
	if startErr == nil {
		_ = proc.Close()

		require.Fail(t, "a file without its execute bit cannot start")
	}

	assert.ErrorIs(t, startErr, invoke.ErrNotFound,
		"Start must read the same input the way LookPath reads it")
}

// TestLookPathOfADirectoryIsNotFound pins the remaining shape of "did
// not resolve to something runnable": a directory answers a path lookup
// and is not an executable.
func TestLookPathOfADirectoryIsNotFound(t *testing.T) {
	t.Parallel()

	env := newLocalEnv(t)

	_, err := env.LookPath(t.Context(), t.TempDir())
	assert.ErrorIs(t, err, invoke.ErrNotFound,
		"a directory is not something runnable")
}

// TestSignalAfterExitMatchesProcessDone pins the benign race every
// Signal caller runs: the process may exit first, and the caller needs
// a sentinel to match, not a string.
func TestSignalAfterExitMatchesProcessDone(t *testing.T) {
	t.Parallel()

	env := newLocalEnv(t)

	proc, err := env.Start(t.Context(), invoke.New("true"), invoke.IO{})
	require.NoError(t, err)

	t.Cleanup(func() { _ = proc.Close() })

	_, err = proc.Wait()
	require.NoError(t, err)

	err = proc.Signal(invoke.SIGTERM)
	require.Error(t, err, "signaling a gone process must error")

	assert.ErrorIs(t, err, os.ErrProcessDone,
		"the benign outcome of the signal-versus-exit race needs a sentinel, not a string")
}
