//go:build docker

package docker_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDownloadFollowsAStandaloneLink checks a link downloaded on its own
// under SymlinkFollow arrives as the file it leads to, as it does from the
// other providers. The archive the daemon sends for a link holds only the
// link, so the path has to be resolved before it is asked for.
func TestDownloadFollowsAStandaloneLink(t *testing.T) {
	t.Parallel()

	env := dialContainer(t)

	src := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, "real.txt"), []byte("followed"), 0o600))
	require.NoError(t, os.Symlink("real.txt", filepath.Join(src, "link.txt")))
	require.NoError(t, env.Upload(t.Context(), src, "/tmp/tree"), "seeding Upload")

	local := filepath.Join(t.TempDir(), "link.txt")
	require.NoError(t,
		env.Download(t.Context(), "/tmp/tree/link.txt", local, invoke.WithSymlinks(invoke.SymlinkFollow)))

	info, err := os.Lstat(local)
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular(), "a followed link must arrive as a regular file")

	got, err := os.ReadFile(local)
	require.NoError(t, err)
	assert.Equal(t, "followed", string(got))
}
