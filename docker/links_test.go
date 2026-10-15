package docker

import (
	"archive/tar"
	"context"
	"os"
	"path/filepath"
	"sync"
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

// extractFollowing extracts an archive whose top entry is "tree", from the
// container path /srv/tree, with links settled under cfg.
func extractFollowing(t *testing.T, entries []tarEntry, cfg invoke.TransferConfig) (string, error) {
	t.Helper()

	dst := t.TempDir()

	err := extractTree(t.Context(), buildArchive(t, entries), dst, cfg,
		linkSettler(filepath.Join(dst, "tree"), "/srv/tree", cfg))

	return filepath.Join(dst, "tree"), err
}

func TestFollowReplacesLinksWithContent(t *testing.T) {
	t.Parallel()

	mode := os.FileMode(0o600)

	staged, err := extractFollowing(t, []tarEntry{
		{name: "tree/", typeflag: tar.TypeDir, mode: 0o755},
		{name: "tree/real.txt", typeflag: tar.TypeReg, body: "real", mode: 0o640},
		{name: "tree/link.txt", typeflag: tar.TypeSymlink, linkname: "real.txt"},
	}, invoke.NewTransferConfig(invoke.WithSymlinks(invoke.SymlinkFollow), invoke.WithMode(mode)))
	require.NoError(t, err)

	info, err := os.Lstat(filepath.Join(staged, "link.txt"))
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular(), "a followed link must become a regular file")
	assert.Equal(t, mode, info.Mode().Perm(), "the mode override applies to the followed file")

	got, err := os.ReadFile(filepath.Join(staged, "link.txt"))
	require.NoError(t, err)
	assert.Equal(t, "real", string(got))
}

// TestFollowSettlesBeforeModesApply checks links are settled while the
// tree is still workable: a read-only directory and a write-only mode
// override are applied only afterwards.
func TestFollowSettlesBeforeModesApply(t *testing.T) {
	t.Parallel()

	writeOnly := os.FileMode(0o200)

	staged, err := extractFollowing(t, []tarEntry{
		{name: "tree/", typeflag: tar.TypeDir, mode: 0o755},
		{name: "tree/locked/", typeflag: tar.TypeDir, mode: 0o555},
		{name: "tree/real.txt", typeflag: tar.TypeReg, body: "real"},
		{name: "tree/locked/link.txt", typeflag: tar.TypeSymlink, linkname: "../real.txt"},
	}, invoke.NewTransferConfig(invoke.WithSymlinks(invoke.SymlinkFollow), invoke.WithMode(writeOnly)))
	require.NoError(t, err, "settling must not be blocked by the modes the tree will carry")

	t.Cleanup(func() { _ = os.Chmod(filepath.Join(staged, "locked"), 0o755) })

	info, err := os.Stat(filepath.Join(staged, "locked"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o555), info.Mode().Perm(), "the directory keeps the mode its header carries")

	info, err = os.Lstat(filepath.Join(staged, "locked", "link.txt"))
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular(), "the link in the read-only directory was followed")
	assert.Equal(t, writeOnly, info.Mode().Perm(), "the override applies once settling is done")
}

func TestFollowReportsProgressForFollowedFiles(t *testing.T) {
	t.Parallel()

	var (
		mu     sync.Mutex
		totals = map[string]int64{}
	)

	cfg := invoke.NewTransferConfig(invoke.WithSymlinks(invoke.SymlinkFollow),
		invoke.WithProgress(func(p invoke.TransferProgress) {
			mu.Lock()
			defer mu.Unlock()

			if p.Current == p.Total {
				totals[p.Path] = p.Total
			}
		}))

	_, err := extractFollowing(t, []tarEntry{
		{name: "tree/", typeflag: tar.TypeDir, mode: 0o755},
		{name: "tree/real.txt", typeflag: tar.TypeReg, body: "real"},
		{name: "tree/link.txt", typeflag: tar.TypeSymlink, linkname: "real.txt"},
	}, cfg)
	require.NoError(t, err)

	assert.Equal(t, map[string]int64{"real.txt": 4, "link.txt": 4}, totals,
		"a followed file is copied, and its progress must say so")
}

func TestFollowStopsWhenCanceled(t *testing.T) {
	t.Parallel()

	dst := t.TempDir()
	cfg := invoke.NewTransferConfig(invoke.WithSymlinks(invoke.SymlinkFollow))

	ctx, cancel := context.WithCancel(t.Context())

	archive := buildArchive(t, []tarEntry{
		{name: "tree/", typeflag: tar.TypeDir, mode: 0o755},
		{name: "tree/real.txt", typeflag: tar.TypeReg, body: "real"},
		{name: "tree/link.txt", typeflag: tar.TypeSymlink, linkname: "real.txt"},
	})

	// Canceled once extraction is done and settling is about to start.
	settle := linkSettler(filepath.Join(dst, "tree"), "/srv/tree", cfg)

	err := extractTree(ctx, archive, dst, cfg, func(ctx context.Context, modes map[string]os.FileMode) error {
		cancel()

		return settle(ctx, modes)
	})
	require.ErrorIs(t, err, context.Canceled, "settling must stop on a canceled context")
}

// TestResolveStagedDoesNotLookThroughALinkedParent checks a link standing
// in a parent component is followed in the container's terms rather than
// by the host.
//
// Here src is a real directory on this host as well, holding a secret
// where the container path would lead. Asking the host about the staged
// path a-leak names walks through b-pivot, a link whose absolute target
// the host resolves on its own filesystem, and reaches that secret.
func TestResolveStagedDoesNotLookThroughALinkedParent(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(src, "z-target"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(src, "z-target", "secret.txt"), []byte("host only"), 0o600))

	staged := filepath.Join(t.TempDir(), "tree")
	require.NoError(t, os.Mkdir(staged, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(staged, "z-target"), []byte("staged file"), 0o600))
	require.NoError(t, os.Symlink(filepath.ToSlash(filepath.Join(src, "z-target")), filepath.Join(staged, "b-pivot")))
	require.NoError(t, os.Symlink("b-pivot/secret.txt", filepath.Join(staged, "a-leak")))

	got, err := resolveStaged(staged, filepath.ToSlash(src), filepath.ToSlash(src)+"/a-leak")
	require.Error(t, err, "resolved to %q", got)
	assert.ErrorContains(t, err, "not a directory",
		"in the container, b-pivot leads to a file, so nothing lies beneath it")
}

func TestSkipRemovesLinks(t *testing.T) {
	t.Parallel()

	staged, err := extractFollowing(t, []tarEntry{
		{name: "tree/", typeflag: tar.TypeDir, mode: 0o755},
		{name: "tree/real.txt", typeflag: tar.TypeReg, body: "real"},
		{name: "tree/link.txt", typeflag: tar.TypeSymlink, linkname: "real.txt"},
	}, invoke.NewTransferConfig(invoke.WithSymlinks(invoke.SymlinkSkip)))
	require.NoError(t, err)

	_, err = os.Lstat(filepath.Join(staged, "link.txt"))
	assert.True(t, os.IsNotExist(err), "a skipped link must not arrive")
	assert.FileExists(t, filepath.Join(staged, "real.txt"))
}
