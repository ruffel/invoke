package fake

import (
	"io"
	"io/fs"
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
