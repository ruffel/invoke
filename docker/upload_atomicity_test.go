//go:build docker

package docker_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/docker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUploadStagesOnTheDestinationFilesystem pins that an upload does not
// depend on /tmp, and by extension that its move into place is a rename on
// the destination's own filesystem rather than a copy across a boundary.
//
// The container's /tmp is mounted read-only. Staging there — as a fixed
// staging path once did — fails outright; staging in the destination's own
// directory, which is writable, does not. A move that has to cross from
// /tmp to the destination is also the move that cannot be atomic, so the
// same change removes both problems.
func TestUploadStagesOnTheDestinationFilesystem(t *testing.T) {
	t.Parallel()

	id := startContainerWith(t, &container.HostConfig{
		Tmpfs: map[string]string{"/tmp": "ro"},
	})

	env, err := docker.New(t.Context(), id)
	require.NoError(t, err, "docker.New")

	t.Cleanup(func() { _ = env.Close() })

	local := filepath.Join(t.TempDir(), "payload.txt")
	require.NoError(t, os.WriteFile(local, []byte("delivered"), 0o600), "writing fixture")

	// /data is on the writable root filesystem, not the read-only /tmp.
	require.NoError(t, env.Upload(t.Context(), local, "/data/payload.txt"),
		"upload must stage on the destination's filesystem, not depend on /tmp being writable")

	_, stdout, _, err := invoke.NewExecutor(env).Output(t.Context(), invoke.New("cat", "/data/payload.txt"))
	require.NoError(t, err, "reading the uploaded file")

	assert.Equal(t, "delivered", string(stdout), "the upload did not arrive at the destination")
}

// TestUploadReplacesAnExistingDirectory pins that uploading a directory
// over one already there replaces it rather than merging into it, and
// leaves nothing of the old contents behind — the behaviour the aside-move
// has to preserve now that it no longer removes the destination up front.
func TestUploadReplacesAnExistingDirectory(t *testing.T) {
	t.Parallel()

	id := startContainer(t)

	env, err := docker.New(t.Context(), id)
	require.NoError(t, err, "docker.New")

	t.Cleanup(func() { _ = env.Close() })

	exec := invoke.NewExecutor(env)

	// An existing destination directory with a file the new tree does not
	// contain.
	_, err = exec.Run(t.Context(),
		invoke.Shell("mkdir -p /data/target && echo stale > /data/target/old.txt"), invoke.IO{})
	require.NoError(t, err, "seeding the existing destination")

	srcDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "new.txt"), []byte("fresh"), 0o600), "writing fixture")

	require.NoError(t, env.Upload(t.Context(), srcDir, "/data/target"), "upload over an existing directory")

	_, stdout, _, err := exec.Output(t.Context(), invoke.New("cat", "/data/target/new.txt"))
	require.NoError(t, err, "reading the replacement content")
	assert.Equal(t, "fresh", string(stdout), "the new content did not arrive")

	// The old file must be gone: a replacement, not a merge.
	_, err = exec.Run(t.Context(), invoke.New("test", "-e", "/data/target/old.txt"), invoke.IO{})
	require.Error(t, err, "the destination kept a file from before the upload; it was merged, not replaced")
}
