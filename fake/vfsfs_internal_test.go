package fake

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The engine relies on a few properties of the filesystem it is handed
// that no contract asks about directly: an exclusive create is exclusive,
// a rename takes a whole subtree and replaces a file in one step, a
// non-empty directory is not removed, and resolution follows links in
// every component.

func TestVFSFSCreateIsExclusiveAndCommitsOnClose(t *testing.T) {
	t.Parallel()

	f := vfsFS{newVFS()}

	w, err := f.CreateExclusive("/tmp/a")
	require.NoError(t, err)

	_, err = f.CreateExclusive("/tmp/a")
	require.ErrorIs(t, err, fs.ErrExist, "a second exclusive create must fail")

	_, err = w.Write([]byte("content"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	r, err := f.Open("/tmp/a")
	require.NoError(t, err)

	got, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, "content", string(got))
}

func TestVFSFSRenameMovesASubtreeAndReplacesAFile(t *testing.T) {
	t.Parallel()

	v := newVFS()
	f := vfsFS{v}

	require.NoError(t, v.mkdirAll("/tmp/src/deep"))
	require.NoError(t, v.writeFile("/tmp/src/deep/f", []byte("deep"), 0o644))
	require.NoError(t, f.Rename("/tmp/src", "/tmp/dst"))

	_, err := f.Lstat("/tmp/src/deep/f")
	require.ErrorIs(t, err, fs.ErrNotExist, "nothing may stay behind at the old name")

	node, ok := v.snapshot("/tmp/dst/deep/f")
	require.True(t, ok, "the subtree must move with its directory")
	assert.Equal(t, "deep", string(node.content))

	require.NoError(t, v.writeFile("/tmp/old", []byte("old"), 0o644))
	require.NoError(t, v.writeFile("/tmp/new", []byte("new"), 0o644))
	require.NoError(t, f.Rename("/tmp/new", "/tmp/old"))

	node, _ = v.snapshot("/tmp/old")
	assert.Equal(t, "new", string(node.content), "a rename replaces the file at its destination")

	require.Error(t, f.Rename("/tmp/old", "/tmp/dst"), "a rename never replaces a directory")
}

func TestVFSFSRemoveRefusesANonEmptyDirectory(t *testing.T) {
	t.Parallel()

	v := newVFS()
	f := vfsFS{v}

	require.NoError(t, v.mkdirAll("/tmp/d"))
	require.NoError(t, v.writeFile("/tmp/d/f", nil, 0o644))

	require.Error(t, f.Remove("/tmp/d"))
	require.NoError(t, f.Remove("/tmp/d/f"))
	require.NoError(t, f.Remove("/tmp/d"))
}

func TestVFSFSResolveFollowsLinksInEveryComponent(t *testing.T) {
	t.Parallel()

	v := newVFS()
	f := vfsFS{v}

	require.NoError(t, v.mkdirAll("/srv/release-1"))
	require.NoError(t, v.writeFile("/srv/release-1/app", nil, 0o755))
	require.NoError(t, v.symlink("/srv/release-1", "/srv/current"))
	require.NoError(t, v.symlink("current/app", "/srv/app"))
	require.NoError(t, v.symlink("loop-b", "/tmp/loop-a"))
	require.NoError(t, v.symlink("loop-a", "/tmp/loop-b"))

	got, err := f.Resolve("/srv/app")
	require.NoError(t, err)
	assert.Equal(t, "/srv/release-1/app", got, "a link through a linked directory resolves to the file")

	got, err = f.Resolve("/srv/current/app")
	require.NoError(t, err)
	assert.Equal(t, "/srv/release-1/app", got, "a linked middle component is followed")

	_, err = f.Resolve("/tmp/loop-a")
	require.ErrorContains(t, err, "too many levels")

	_, err = f.Resolve("/srv/missing")
	require.ErrorIs(t, err, fs.ErrNotExist)
}

// linkedRelease lays out the deploy pattern: a stable name linked to the
// current release directory, with a nested file in it.
func linkedRelease(t *testing.T) *Environment {
	t.Helper()

	e := New()

	t.Cleanup(func() { _ = e.Close() })

	require.NoError(t, e.fs.mkdirAll("/srv/release-1/sub"))
	require.NoError(t, e.fs.writeFile("/srv/release-1/app", []byte("app"), 0o644))
	require.NoError(t, e.fs.writeFile("/srv/release-1/sub/deep", []byte("deep"), 0o644))
	require.NoError(t, e.fs.symlink("/srv/release-1", "/srv/current"))

	return e
}

// TestTransfersFollowALinkedParent checks transfers through a linked
// directory reach what it links to, as the host's filesystem does. The
// virtual filesystem keys entries by their literal path, so a lookup that
// does not resolve the parent finds nothing under the link.
func TestTransfersFollowALinkedParent(t *testing.T) {
	t.Parallel()

	t.Run("download a file under it", func(t *testing.T) {
		t.Parallel()

		e := linkedRelease(t)
		local := filepath.Join(t.TempDir(), "app")

		require.NoError(t, e.Download(t.Context(), "/srv/current/app", local))
		assert.Equal(t, "app", readHost(t, local))
	})

	t.Run("download a subtree under it", func(t *testing.T) {
		t.Parallel()

		e := linkedRelease(t)
		local := filepath.Join(t.TempDir(), "sub")

		require.NoError(t, e.Download(t.Context(), "/srv/current/sub", local))
		assert.Equal(t, "deep", readHost(t, filepath.Join(local, "deep")))
	})

	t.Run("upload a tree onto it", func(t *testing.T) {
		t.Parallel()

		e := linkedRelease(t)

		src := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(src, "new"), []byte("new"), 0o644))
		require.NoError(t, os.Mkdir(filepath.Join(src, "more"), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(src, "more", "f"), []byte("f"), 0o644))

		require.NoError(t, e.Upload(t.Context(), src, "/srv/current"))

		node, ok := e.fs.snapshot("/srv/release-1/new")
		require.True(t, ok, "the tree must land in the release the link points at")
		assert.Equal(t, "new", string(node.content))

		_, ok = e.fs.snapshot("/srv/release-1/more/f")
		assert.True(t, ok, "a nested directory must land there too")

		_, ok = e.fs.snapshot("/srv/release-1/app")
		assert.True(t, ok, "and merge with what the release held")
	})

	t.Run("chmod reaches the linked directory", func(t *testing.T) {
		t.Parallel()

		e := linkedRelease(t)

		src := t.TempDir()
		require.NoError(t, os.Chmod(src, 0o700))

		require.NoError(t, e.Upload(t.Context(), src, "/srv/current"))

		node, ok := e.fs.snapshot("/srv/release-1")
		require.True(t, ok)
		assert.Equal(t, fs.FileMode(0o700), node.mode.Perm(), "the root's mode belongs to the directory, not the link")
	})
}

// TestUploadRefusesALinkedChildDirectory checks resolving parents did not
// loosen the engine's refusal: a link standing where the tree has a child
// directory is still refused, not written through.
func TestUploadRefusesALinkedChildDirectory(t *testing.T) {
	t.Parallel()

	e := linkedRelease(t)

	require.NoError(t, e.fs.mkdirAll("/srv/target"))
	require.NoError(t, e.fs.symlink("/srv/release-1", "/srv/target/sub"))

	src := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(src, "sub"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(src, "sub", "planted"), []byte("x"), 0o644))

	err := e.Upload(t.Context(), src, "/srv/target")
	require.ErrorContains(t, err, "symbolic link", "a linked child directory must be refused")

	_, ok := e.fs.snapshot("/srv/release-1/planted")
	assert.False(t, ok, "nothing may be written through the link")
}

func readHost(t *testing.T, p string) string {
	t.Helper()

	got, err := os.ReadFile(p)
	require.NoError(t, err)

	return string(got)
}
