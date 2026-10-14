//go:build docker

package docker_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/docker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContainerWithoutAShell checks what a container with no shell is
// offered: commands still run, and everything that needs a shell is
// refused as unsupported rather than accepted and quietly not done.
//
// Signal delivery, path lookup and staging a transfer are all built on a
// shell in the container. The failure worth preventing is not the
// refusal but its absence — a signal reported as sent that reached
// nothing, or a transfer that half-arrived.
func TestContainerWithoutAShell(t *testing.T) {
	t.Parallel()

	id := startContainer(t)
	removeShell(t, id)

	env, err := docker.New(t.Context(), id)
	require.NoError(t, err, "a container without a shell must still be reachable")

	t.Cleanup(func() { _ = env.Close() })

	assert.False(t, env.Capabilities().Signals,
		"signals must not be declared where there is no shell to deliver them")

	_, err = env.LookPath(t.Context(), "cat")
	require.ErrorIs(t, err, invoke.ErrNotSupported, "path lookup needs a shell and must say so")

	local := filepath.Join(t.TempDir(), "payload.txt")
	require.NoError(t, os.WriteFile(local, []byte("payload"), 0o600), "writing fixture")

	require.ErrorIs(t, env.Upload(t.Context(), local, "/payload.txt"), invoke.ErrNotSupported,
		"an upload needs a shell to stage it and must say so")
	require.ErrorIs(t, env.Download(t.Context(), "/etc/hostname", filepath.Join(t.TempDir(), "hostname")),
		invoke.ErrNotSupported, "a download is refused on the same terms as an upload")

	// A command held open by its input, so it is certainly still running
	// when the signal is asked for.
	input, feed := io.Pipe()

	// Registered after the environment's own cleanup, so it runs first:
	// with no shell there is no way to stop the command but to end its
	// input, and closing the environment waits for it.
	t.Cleanup(func() { _ = feed.Close() })

	var stdout bytes.Buffer

	proc, err := env.Start(t.Context(), invoke.New("cat"), invoke.IO{Stdin: input, Stdout: &stdout})
	require.NoError(t, err, "a command needs no shell to run")

	require.ErrorIs(t, proc.Signal(invoke.SIGTERM), invoke.ErrNotSupported,
		"a signal that cannot be delivered must be refused, not reported as sent")

	_, err = io.WriteString(feed, "still running\n")
	require.NoError(t, err, "writing to the command's input")
	require.NoError(t, feed.Close(), "ending the command's input")

	_, err = proc.Wait()
	require.NoError(t, err, "the command must run to its own end after a refused signal")

	assert.Equal(t, "still running\n", stdout.String(),
		"the command's output must arrive as it does with a shell present")
}

// removeShell deletes the container's shell and leaves its other tools
// in place: in the test image they are one binary under several names.
func removeShell(t *testing.T, id string) {
	t.Helper()

	env, err := docker.New(t.Context(), id)
	require.NoError(t, err, "docker.New")

	defer func() { _ = env.Close() }()

	_, err = runInContainer(t, env, "rm", "/bin/sh")
	require.NoError(t, err, "removing the shell")
}
