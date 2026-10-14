//go:build docker

package docker_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/docker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContainerWithoutAShell checks what a container with no shell is
// offered: commands still run, downloads still arrive, and everything
// that needs a shell is refused as unsupported rather than accepted and
// quietly not done.
//
// Signal delivery, path lookup and staging an upload are all built on a
// shell in the container. The failure worth preventing is not the
// refusal but its absence — a signal reported as sent that reached
// nothing, or a transfer that half-arrived. A download needs no shell:
// the daemon hands over an archive and it is unpacked on this side.
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

	downloaded := filepath.Join(t.TempDir(), "hostname")
	require.NoError(t, env.Download(t.Context(), "/etc/hostname", downloaded),
		"a download needs no shell in the container")

	got, err := os.ReadFile(downloaded)
	require.NoError(t, err, "reading the download")
	assert.NotEmpty(t, got, "the downloaded file must carry the container's content")

	// A command held open by its input, so it is certainly still running
	// when the signal is asked for.
	input, feed := io.Pipe()

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

// TestShelllessCloseAndCancelDoNotWaitForTheCommand checks Close and
// cancellation return promptly in a container without a shell, though
// they cannot stop the command.
//
// There is no signalling a command there: the wrapper that records its
// id, and any kill, need a shell. Both used to wait for the command to
// finish on its own, so a long command held Close or a canceled context
// for its whole life. Now they let it go, and the outcome says that is
// what happened rather than claiming it was ended.
func TestShelllessCloseAndCancelDoNotWaitForTheCommand(t *testing.T) {
	t.Parallel()

	const bound = 10 * time.Second

	id := startContainer(t)
	removeShell(t, id)

	env, err := docker.New(t.Context(), id)
	require.NoError(t, err, "docker.New")

	t.Cleanup(func() { _ = env.Close() })

	t.Run("close", func(t *testing.T) {
		t.Parallel()

		proc, err := env.Start(t.Context(), invoke.New("sleep", "300"), invoke.IO{})
		require.NoError(t, err, "a command needs no shell to run")

		closed := make(chan struct{})

		go func() {
			_ = proc.Close()

			close(closed)
		}()

		select {
		case <-closed:
		case <-time.After(bound):
			t.Fatal("Close waited for a command it had no way to stop")
		}

		_, err = proc.Wait()
		require.ErrorIs(t, err, invoke.ErrClosed, "a command let go by Close")
		assert.Contains(t, err.Error(), "abandoned", "the outcome must not claim the command was ended")
	})

	t.Run("cancel", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())

		proc, err := env.Start(ctx, invoke.New("sleep", "300"), invoke.IO{})
		require.NoError(t, err, "a command needs no shell to run")

		t.Cleanup(func() { _ = proc.Close() })

		cancel()

		waited := make(chan error, 1)

		go func() {
			_, err := proc.Wait()
			waited <- err
		}()

		select {
		case err := <-waited:
			require.ErrorIs(t, err, context.Canceled, "a command let go by cancellation")
		case <-time.After(bound):
			t.Fatal("Wait outlived its canceled context for a command it had no way to stop")
		}
	})

	t.Run("finished-first", func(t *testing.T) {
		t.Parallel()

		proc, err := env.Start(t.Context(), invoke.New("false"), invoke.IO{})
		require.NoError(t, err, "a command needs no shell to run")

		// Let it finish before Close lets go of it, so there is an exit
		// to report rather than an abandonment.
		time.Sleep(time.Second)

		require.NoError(t, proc.Close())

		_, err = proc.Wait()

		var exitErr *invoke.ExitError

		require.ErrorAs(t, err, &exitErr, "a command that finished keeps its own outcome")
		assert.Equal(t, 1, exitErr.Code)
	})
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
