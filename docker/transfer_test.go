//go:build docker

package docker_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/docker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// remotePath returns a container path unique to this test.
func remotePath(t *testing.T) string {
	t.Helper()

	return "/tmp/invoke-test-" + strings.ReplaceAll(t.Name(), "/", "-")
}

// TestTrailingSeparatorIsRejected checks an ambiguous destination is
// refused rather than guessed at. "Into this directory" and "as this
// name" are different transfers, and picking one silently puts the
// caller's data somewhere they did not ask for.
func TestTrailingSeparatorIsRejected(t *testing.T) {
	t.Parallel()

	env := dialContainer(t)

	src := filepath.Join(t.TempDir(), "file.txt")
	require.NoError(t, os.WriteFile(src, []byte("payload"), 0o600))

	err := env.Upload(t.Context(), src, remotePath(t)+"/")
	require.Error(t, err, "a destination ending in a separator must be refused")
	assert.ErrorContains(t, err, "separator", "the error should say what is wrong with the path")
}

// TestUploadDoesNotCarryHostOwnership checks files arrive owned by the
// container's own account rather than by whichever unrelated account
// happens to hold the host's numeric id there.
func TestUploadDoesNotCarryHostOwnership(t *testing.T) {
	t.Parallel()

	env := dialContainer(t)

	src := filepath.Join(t.TempDir(), "file.txt")
	require.NoError(t, os.WriteFile(src, []byte("payload"), 0o600))

	dst := remotePath(t)
	require.NoError(t, env.Upload(t.Context(), src, dst))

	out, err := runInContainer(t, env, "stat", "-c", "%u:%g", dst)
	require.NoError(t, err)

	assert.Equal(t, "0:0", strings.TrimSpace(out),
		"the host's numeric ids must not be carried into the container")
}

// TestDownloadPreservesSymlinks checks links survive the journey out of
// the container as links. An archive can carry them, and dropping them
// yields a transfer that reports success while quietly losing entries.
func TestDownloadPreservesSymlinks(t *testing.T) {
	t.Parallel()

	env := dialContainer(t)

	dst := remotePath(t)
	_, err := runInContainer(t, env, "sh", "-c",
		"mkdir -p "+dst+" && printf real > "+dst+"/real.txt && "+
			"ln -s real.txt "+dst+"/link.txt && ln -s gone "+dst+"/dangling.txt")
	require.NoError(t, err)

	local := filepath.Join(t.TempDir(), "downloaded")
	require.NoError(t, env.Download(t.Context(), dst, local))

	target, err := os.Readlink(filepath.Join(local, "link.txt"))
	require.NoError(t, err, "link.txt did not arrive as a link")
	assert.Equal(t, "real.txt", target)

	dangling, err := os.Readlink(filepath.Join(local, "dangling.txt"))
	require.NoError(t, err, "a dangling link must survive as a link")
	assert.Equal(t, "gone", dangling)
}

// TestDownloadAppliesDirectoryModes checks a downloaded directory keeps
// the mode it had in the container instead of a fixed default.
func TestDownloadAppliesDirectoryModes(t *testing.T) {
	t.Parallel()

	env := dialContainer(t)

	dst := remotePath(t)
	_, err := runInContainer(t, env, "sh", "-c",
		"mkdir -p "+dst+"/inner && printf x > "+dst+"/inner/f.txt && chmod 0700 "+dst+"/inner")
	require.NoError(t, err)

	local := filepath.Join(t.TempDir(), "downloaded")
	require.NoError(t, env.Download(t.Context(), dst, local))

	info, err := os.Stat(filepath.Join(local, "inner"))
	require.NoError(t, err)

	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(),
		"the directory's own mode must survive the download")
}

// TestProgressPathsAreRelativeToTheRoot checks a nested file reports a
// path relative to the transfer root, not to the archive that carried it.
func TestProgressPathsAreRelativeToTheRoot(t *testing.T) {
	t.Parallel()

	env := dialContainer(t)

	srcDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "top.txt"), []byte("top"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(srcDir, "nested"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "nested", "deep.txt"), []byte("deep"), 0o600))

	seen := make(map[string]bool)

	require.NoError(t, env.Upload(t.Context(), srcDir, remotePath(t),
		invoke.WithProgress(func(p invoke.TransferProgress) { seen[p.Path] = true })))

	assert.True(t, seen["top.txt"], "progress paths seen: %v", seen)
	assert.True(t, seen["nested/deep.txt"],
		"a nested file must report a path relative to the transfer root; seen: %v", seen)
}

// TestUploadRefusesRelativeRemotePath checks a container path that is not
// absolute is refused, rather than resolved against a working directory
// the caller never chose.
func TestUploadRefusesRelativeRemotePath(t *testing.T) {
	t.Parallel()

	env := dialContainer(t)

	src := filepath.Join(t.TempDir(), "file.txt")
	require.NoError(t, os.WriteFile(src, []byte("payload"), 0o600))

	err := env.Upload(t.Context(), src, "relative/path.txt")
	assert.ErrorContains(t, err, "absolute", "a relative container path must be refused")
}

// runInContainer runs a command in the container and returns its stdout.
func runInContainer(t *testing.T, env *docker.Environment, name string, args ...string) (string, error) {
	t.Helper()

	var out strings.Builder

	proc, err := env.Start(t.Context(), invoke.New(name, args...), invoke.IO{Stdout: &out})
	if err != nil {
		return "", err
	}

	_, waitErr := proc.Wait()

	return out.String(), waitErr
}
