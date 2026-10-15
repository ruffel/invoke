package docker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writePlaceFixture creates path's parents and writes content there.
func writePlaceFixture(t *testing.T, path, content string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func readPlaced(t *testing.T, path string) string {
	t.Helper()

	got, err := os.ReadFile(path)
	require.NoError(t, err)

	return string(got)
}

func TestPlaceArrivalMergesATreeIntoAnExistingDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	src := filepath.Join(dir, "staging", "arrived")
	writePlaceFixture(t, filepath.Join(src, "shared.txt"), "new")
	writePlaceFixture(t, filepath.Join(src, "sub", "added.txt"), "added")

	dst := filepath.Join(dir, "dest")
	writePlaceFixture(t, filepath.Join(dst, "shared.txt"), "old")
	writePlaceFixture(t, filepath.Join(dst, "keep.txt"), "kept")
	writePlaceFixture(t, filepath.Join(dst, "sub", "keep.txt"), "kept deep")

	require.NoError(t, placeArrival(src, dst))

	assert.Equal(t, "new", readPlaced(t, filepath.Join(dst, "shared.txt")), "a namesake is replaced")
	assert.Equal(t, "kept", readPlaced(t, filepath.Join(dst, "keep.txt")), "what only the destination held stays")
	assert.Equal(t, "kept deep", readPlaced(t, filepath.Join(dst, "sub", "keep.txt")), "and stays at depth")
	assert.Equal(t, "added", readPlaced(t, filepath.Join(dst, "sub", "added.txt")))
}

func TestPlaceArrivalReplacesAFileOrMovesIntoNothing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	src := filepath.Join(dir, "staging", "file")
	writePlaceFixture(t, src, "new")

	dst := filepath.Join(dir, "file")
	writePlaceFixture(t, dst, "old")

	require.NoError(t, placeArrival(src, dst))
	assert.Equal(t, "new", readPlaced(t, dst), "a file replaces its namesake")

	tree := filepath.Join(dir, "staging", "tree")
	writePlaceFixture(t, filepath.Join(tree, "a.txt"), "a")

	require.NoError(t, placeArrival(tree, filepath.Join(dir, "fresh")))
	assert.Equal(t, "a", readPlaced(t, filepath.Join(dir, "fresh", "a.txt")), "a tree moves into a missing destination")
}

// TestPlaceArrivalRefusesToDeleteWhatStandsInTheWay checks a mismatch
// between what arrived and what is there is an error, never a deletion.
func TestPlaceArrivalRefusesToDeleteWhatStandsInTheWay(t *testing.T) {
	t.Parallel()

	t.Run("tree onto a file", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		src := filepath.Join(dir, "staging", "tree")
		writePlaceFixture(t, filepath.Join(src, "a.txt"), "a")

		dst := filepath.Join(dir, "dest")
		writePlaceFixture(t, dst, "precious")

		require.Error(t, placeArrival(src, dst))
		assert.Equal(t, "precious", readPlaced(t, dst))
	})

	t.Run("file onto a directory", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		src := filepath.Join(dir, "staging", "file")
		writePlaceFixture(t, src, "new")

		dst := filepath.Join(dir, "dest")
		writePlaceFixture(t, filepath.Join(dst, "keep.txt"), "precious")

		require.Error(t, placeArrival(src, dst))
		assert.Equal(t, "precious", readPlaced(t, filepath.Join(dst, "keep.txt")))
	})

	t.Run("tree through a linked child directory", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		src := filepath.Join(dir, "staging", "tree")
		writePlaceFixture(t, filepath.Join(src, "sub", "a.txt"), "a")

		elsewhere := filepath.Join(dir, "elsewhere")
		require.NoError(t, os.Mkdir(elsewhere, 0o755))

		dst := filepath.Join(dir, "dest")
		require.NoError(t, os.Mkdir(dst, 0o755))
		require.NoError(t, os.Symlink(elsewhere, filepath.Join(dst, "sub")))

		require.ErrorContains(t, placeArrival(src, dst), "symbolic link")

		_, err := os.Stat(filepath.Join(elsewhere, "a.txt"))
		assert.True(t, os.IsNotExist(err), "nothing may be written through the link")
	})
}

// TestPlaceArrivalMergesIntoALinkedRoot checks the root itself may be a
// link to a directory, the ordinary way to point a stable name at a
// release, and is merged through.
func TestPlaceArrivalMergesIntoALinkedRoot(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	src := filepath.Join(dir, "staging", "tree")
	writePlaceFixture(t, filepath.Join(src, "a.txt"), "a")

	release := filepath.Join(dir, "release-1")
	writePlaceFixture(t, filepath.Join(release, "keep.txt"), "kept")

	current := filepath.Join(dir, "current")
	require.NoError(t, os.Symlink(release, current))

	require.NoError(t, placeArrival(src, current))
	assert.Equal(t, "a", readPlaced(t, filepath.Join(release, "a.txt")))
	assert.Equal(t, "kept", readPlaced(t, filepath.Join(release, "keep.txt")))
}

// TestPlaceArrivalMovesOutOfReadOnlyStaging checks a staged directory
// whose archive mode is read-only can still be emptied into place, and
// that the destination is given that mode once it has been.
func TestPlaceArrivalMovesOutOfReadOnlyStaging(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	src := filepath.Join(dir, "staging", "tree")
	writePlaceFixture(t, filepath.Join(src, "a.txt"), "a")
	require.NoError(t, os.Chmod(src, 0o555))

	dst := filepath.Join(dir, "dest")
	writePlaceFixture(t, filepath.Join(dst, "keep.txt"), "kept")

	t.Cleanup(func() {
		_ = os.Chmod(src, 0o755)
		_ = os.Chmod(dst, 0o755)
	})

	require.NoError(t, placeArrival(src, dst))
	assert.Equal(t, "a", readPlaced(t, filepath.Join(dst, "a.txt")))

	info, err := os.Stat(dst)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o555), info.Mode().Perm(), "the destination takes the source's mode")
}
