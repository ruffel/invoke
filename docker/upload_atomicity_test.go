//go:build docker

package docker_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/moby/api/types/container"
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

	stdout, err := invoke.NewExecutor(env).Output(t.Context(), invoke.New("cat", "/data/payload.txt"))
	require.NoError(t, err, "reading the uploaded file")

	assert.Equal(t, "delivered", string(stdout), "the upload did not arrive at the destination")
}

// TestUploadMergesIntoAnExistingDirectory pins the container-side merge
// beyond what the contract asks of every provider: the shell that does it
// has its own ways to go wrong. Hidden names have to be globbed for
// explicitly, a link where a file arrives has to be replaced rather than
// moved through, and a directory whose archive mode is read-only still has
// to be emptied into place.
func TestUploadMergesIntoAnExistingDirectory(t *testing.T) {
	t.Parallel()

	env := dialContainer(t)
	exec := invoke.NewExecutor(env)

	_, err := exec.Run(t.Context(), invoke.Shell(
		"mkdir -p /data/target/locked /data/elsewhere && echo kept > /data/target/old.txt && "+
			"ln -s /data/elsewhere /data/target/link.txt"), invoke.IO{})
	require.NoError(t, err, "seeding the existing destination")

	srcDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, ".hidden"), []byte("hidden"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "link.txt"), []byte("file now"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(srcDir, "locked"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "locked", "inner.txt"), []byte("inner"), 0o600))
	require.NoError(t, os.Chmod(filepath.Join(srcDir, "locked"), 0o555))

	t.Cleanup(func() { _ = os.Chmod(filepath.Join(srcDir, "locked"), 0o755) })

	require.NoError(t, env.Upload(t.Context(), srcDir, "/data/target"), "upload over an existing directory")

	for path, want := range map[string]string{
		"/data/target/old.txt":          "kept\n",
		"/data/target/.hidden":          "hidden",
		"/data/target/link.txt":         "file now",
		"/data/target/locked/inner.txt": "inner",
	} {
		got, err := exec.Output(t.Context(), invoke.New("cat", path))
		require.NoErrorf(t, err, "reading %s", path)
		assert.Equalf(t, want, string(got), "%s after the merge", path)
	}

	_, err = exec.Run(t.Context(), invoke.New("test", "!", "-L", "/data/target/link.txt"), invoke.IO{})
	require.NoError(t, err, "the link must have been replaced by the file, not written through")

	_, err = exec.Run(t.Context(), invoke.New("test", "-e", "/data/elsewhere/link.txt"), invoke.IO{})
	require.Error(t, err, "nothing may have been moved into the directory the link pointed at")
}

// TestUploadRefusesALinkedChildDirectory checks a symbolic link standing
// where the tree has a directory is refused, as the engine refuses it,
// rather than having the rest of the tree written through it.
func TestUploadRefusesALinkedChildDirectory(t *testing.T) {
	t.Parallel()

	env := dialContainer(t)
	exec := invoke.NewExecutor(env)

	_, err := exec.Run(t.Context(), invoke.Shell(
		"mkdir -p /data/target /data/elsewhere && ln -s /data/elsewhere /data/target/sub"), invoke.IO{})
	require.NoError(t, err, "seeding the existing destination")

	srcDir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(srcDir, "sub"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "sub", "a.txt"), []byte("a"), 0o600))

	err = env.Upload(t.Context(), srcDir, "/data/target")
	require.ErrorContains(t, err, "symbolic link", "a linked child directory must be refused")

	_, err = exec.Run(t.Context(), invoke.New("test", "-e", "/data/elsewhere/a.txt"), invoke.IO{})
	require.Error(t, err, "nothing may be written through the link")
}
