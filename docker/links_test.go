package docker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stagedTree lays out a downloaded tree as it arrives from the daemon,
// links as links, for the container path src.
func stagedTree(t *testing.T) string {
	t.Helper()

	staged := filepath.Join(t.TempDir(), "tree")
	require.NoError(t, os.MkdirAll(filepath.Join(staged, "sub"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(staged, "real.txt"), []byte("real"), 0o640))

	links := map[string]string{
		"relative.txt":     "real.txt",
		"sub/up.txt":       "../real.txt",
		"absolute.txt":     "/srv/tree/real.txt",
		"chain.txt":        "relative.txt",
		"escape.txt":       "/etc/passwd",
		"escape-up.txt":    "../outside.txt",
		"loop-a.txt":       "loop-b.txt",
		"loop-b.txt":       "loop-a.txt",
		"to-directory.txt": "sub",
	}

	for name, target := range links {
		require.NoError(t, os.Symlink(target, filepath.Join(staged, filepath.FromSlash(name))))
	}

	return staged
}

func TestResolveStagedFollowsLinksInsideTheRoot(t *testing.T) {
	t.Parallel()

	const src = "/srv/tree"

	staged := stagedTree(t)

	for _, link := range []string{"relative.txt", "sub/up.txt", "absolute.txt", "chain.txt"} {
		got, err := resolveStaged(staged, src, src+"/"+link)
		require.NoErrorf(t, err, "resolving %s", link)
		assert.Equalf(t, filepath.Join(staged, "real.txt"), got, "%s must end at the staged real.txt", link)
	}
}

func TestResolveStagedRefusesWhatItCannotFollow(t *testing.T) {
	t.Parallel()

	const src = "/srv/tree"

	staged := stagedTree(t)

	for link, want := range map[string]string{
		"escape.txt":    "outside the transfer root",
		"escape-up.txt": "outside the transfer root",
		"loop-a.txt":    "too many levels",
	} {
		_, err := resolveStaged(staged, src, src+"/"+link)
		require.Errorf(t, err, "resolving %s", link)
		assert.ErrorContainsf(t, err, want, "resolving %s", link)
		assert.ErrorContainsf(t, err, link, "the error must name %s", link)
	}
}

func TestApplyLinkPolicyFollowReplacesLinksWithContent(t *testing.T) {
	t.Parallel()

	staged := filepath.Join(t.TempDir(), "tree")
	require.NoError(t, os.MkdirAll(staged, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(staged, "real.txt"), []byte("real"), 0o640))
	require.NoError(t, os.Symlink("real.txt", filepath.Join(staged, "link.txt")))

	mode := os.FileMode(0o600)
	require.NoError(t, applyLinkPolicy(staged, "/srv/tree",
		invoke.NewTransferConfig(invoke.WithSymlinks(invoke.SymlinkFollow), invoke.WithMode(mode))))

	info, err := os.Lstat(filepath.Join(staged, "link.txt"))
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular(), "a followed link must become a regular file")
	assert.Equal(t, mode, info.Mode().Perm(), "the mode override applies to the followed file")

	got, err := os.ReadFile(filepath.Join(staged, "link.txt"))
	require.NoError(t, err)
	assert.Equal(t, "real", string(got))
}

func TestApplyLinkPolicySkipRemovesLinks(t *testing.T) {
	t.Parallel()

	staged := filepath.Join(t.TempDir(), "tree")
	require.NoError(t, os.MkdirAll(staged, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(staged, "real.txt"), []byte("real"), 0o640))
	require.NoError(t, os.Symlink("real.txt", filepath.Join(staged, "link.txt")))

	require.NoError(t, applyLinkPolicy(staged, "/srv/tree",
		invoke.NewTransferConfig(invoke.WithSymlinks(invoke.SymlinkSkip))))

	_, err := os.Lstat(filepath.Join(staged, "link.txt"))
	assert.True(t, os.IsNotExist(err), "a skipped link must not arrive")
	assert.FileExists(t, filepath.Join(staged, "real.txt"))
}
