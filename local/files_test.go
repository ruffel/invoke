package local_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	modePrivate    = fs.FileMode(0o600)
	modeGrouped    = fs.FileMode(0o640)
	modeDefault    = fs.FileMode(0o644)
	modeRestricted = fs.FileMode(0o550)
	modeTreeDir    = fs.FileMode(0o750)
)

// writeFixture creates a file with content and mode inside dir.
func writeFixture(t *testing.T, dir, name, content string, mode fs.FileMode) string {
	t.Helper()

	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), mode), "writing fixture %q", path)

	// WriteFile is umask-subject; pin the intended mode explicitly.
	require.NoError(t, os.Chmod(path, mode), "chmod fixture %q", path)

	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	content, err := os.ReadFile(path)
	require.NoError(t, err, "reading %q", path)

	return string(content)
}

func fileMode(t *testing.T, path string) fs.FileMode {
	t.Helper()

	info, err := os.Lstat(path)
	require.NoError(t, err, "lstat %q", path)

	return info.Mode().Perm()
}

func TestUploadRoundTripPreservesContentAndMode(t *testing.T) {
	t.Parallel()

	env := newEnv(t)
	src := writeFixture(t, t.TempDir(), "src.txt", "payload", modeGrouped)
	dst := filepath.Join(t.TempDir(), "dst.txt")

	require.NoError(t, env.Upload(t.Context(), src, dst), "Upload")

	assert.Equal(t, "payload", readFile(t, dst), "content")

	// 0640 survives even though a typical umask would clip a fresh
	// create to 0620: modes are applied by chmod, not at open time.
	assert.Equal(t, modeGrouped, fileMode(t, dst), "mode")
}

func TestWithModeAppliesOnOverwrite(t *testing.T) {
	t.Parallel()

	env := newEnv(t)
	src := writeFixture(t, t.TempDir(), "src.txt", "new content", modeDefault)

	dstDir := t.TempDir()
	dst := writeFixture(t, dstDir, "dst.txt", "old content", modeDefault)

	require.NoError(t, env.Upload(t.Context(), src, dst, invoke.WithMode(modePrivate)), "Upload")

	assert.Equal(t, "new content", readFile(t, dst), "content, want overwritten")
	assert.Equal(t, modePrivate, fileMode(t, dst),
		"mode after overwrite: WithMode must beat the pre-existing mode")
}

func TestSamePathIsRejectedAndDataSurvives(t *testing.T) {
	t.Parallel()

	env := newEnv(t)
	src := writeFixture(t, t.TempDir(), "precious.txt", "PRECIOUS DATA", modeDefault)

	require.Error(t, env.Upload(t.Context(), src, src), "Upload(p, p) succeeded; it must be rejected")

	require.Equal(t, "PRECIOUS DATA", readFile(t, src),
		"source content changed after rejected transfer; data was destroyed")
}

func TestDestinationInsideSourceIsRejected(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	srcDir := t.TempDir()
	writeFixture(t, srcDir, "a.txt", "a", modeDefault)

	require.Error(t, env.Upload(t.Context(), srcDir, filepath.Join(srcDir, "copy")),
		"copying a directory into its own subtree succeeded; it must be rejected")
}

func TestFailedTransferLeavesDestinationIntact(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	srcDir := t.TempDir()
	src := writeFixture(t, srcDir, "src.txt", "unreadable", modeDefault)

	require.NoError(t, os.Chmod(src, 0), "chmod")

	t.Cleanup(func() { _ = os.Chmod(src, modeDefault) })

	dstDir := t.TempDir()
	dst := writeFixture(t, dstDir, "dst.txt", "precious destination", modeDefault)

	require.Error(t, env.Upload(t.Context(), src, dst), "Upload of unreadable source succeeded, want error")

	assert.Equal(t, "precious destination", readFile(t, dst),
		"destination changed after failed transfer; atomicity was violated")
}

func TestCanceledTransferLeavesDestinationIntactAndNoTempFiles(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	src := writeFixture(t, t.TempDir(), "src.bin", strings.Repeat("x", 1<<20), modeDefault)

	dstDir := t.TempDir()
	dst := writeFixture(t, dstDir, "dst.bin", "precious destination", modeDefault)

	ctx, cancel := context.WithCancel(t.Context())

	// Cancel from inside the progress callback: the transfer is
	// provably mid-flight when the context dies.
	err := env.Upload(ctx, src, dst, invoke.WithProgress(func(_ invoke.TransferProgress) {
		cancel()
	}))
	require.ErrorIs(t, err, context.Canceled, "Upload under cancellation")

	assert.Equal(t, "precious destination", readFile(t, dst),
		"destination changed after canceled transfer; atomicity was violated")

	leftovers, err := filepath.Glob(filepath.Join(dstDir, ".invoke-*"))
	require.NoError(t, err, "glob")

	assert.Empty(t, leftovers, "temporary files left behind")
}

func TestTreeRoundTrip(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	srcDir := t.TempDir()
	writeFixture(t, srcDir, "top.txt", "top", modeDefault)

	nested := filepath.Join(srcDir, "nested")
	require.NoError(t, os.Mkdir(nested, modeTreeDir), "mkdir")
	require.NoError(t, os.Chmod(nested, modeTreeDir), "chmod")

	writeFixture(t, nested, "deep.txt", "deep", modeGrouped)

	dst := filepath.Join(t.TempDir(), "into", "tree")

	require.NoError(t, env.Upload(t.Context(), srcDir, dst), "Upload")

	assert.Equal(t, "top", readFile(t, filepath.Join(dst, "top.txt")), "top.txt")
	assert.Equal(t, "deep", readFile(t, filepath.Join(dst, "nested", "deep.txt")), "nested/deep.txt")
	assert.Equal(t, modeTreeDir, fileMode(t, filepath.Join(dst, "nested")), "nested dir mode")
	assert.Equal(t, modeGrouped, fileMode(t, filepath.Join(dst, "nested", "deep.txt")), "deep.txt mode")
}

func TestReadOnlySourceDirectoryCopies(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	srcDir := t.TempDir()
	locked := filepath.Join(srcDir, "locked")

	require.NoError(t, os.Mkdir(locked, 0o755), "mkdir")

	writeFixture(t, locked, "inside.txt", "inside", modeDefault)

	require.NoError(t, os.Chmod(locked, modeRestricted), "chmod")

	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	dst := filepath.Join(t.TempDir(), "copy")

	// The copy deliberately ends read-only too; reopen it for TempDir
	// cleanup.
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dst, "locked"), 0o755) })

	require.NoError(t, env.Upload(t.Context(), srcDir, dst), "Upload of read-only dir tree")

	assert.Equal(t, "inside", readFile(t, filepath.Join(dst, "locked", "inside.txt")), "inside.txt")
	assert.Equal(t, modeRestricted, fileMode(t, filepath.Join(dst, "locked")), "locked dir mode")
}

func TestSymlinksArePreservedByDefault(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	srcDir := t.TempDir()
	writeFixture(t, srcDir, "real.txt", "real", modeDefault)

	require.NoError(t, os.Symlink("real.txt", filepath.Join(srcDir, "link.txt")), "symlink")
	require.NoError(t, os.Symlink("gone-target", filepath.Join(srcDir, "dangling.txt")), "symlink")

	dst := filepath.Join(t.TempDir(), "copy")

	require.NoError(t, env.Upload(t.Context(), srcDir, dst), "Upload")

	target, err := os.Readlink(filepath.Join(dst, "link.txt"))
	assert.NoError(t, err, "link.txt: want preserved link to real.txt")
	assert.Equal(t, "real.txt", target, "link.txt: want preserved link to real.txt")

	target, err = os.Readlink(filepath.Join(dst, "dangling.txt"))
	assert.NoError(t, err, "dangling.txt: want the dangling link preserved as-is")
	assert.Equal(t, "gone-target", target, "dangling.txt: want the dangling link preserved as-is")
}

func TestSymlinkFollowCopiesContent(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	srcDir := t.TempDir()
	writeFixture(t, srcDir, "real.txt", "followed", modeDefault)

	require.NoError(t, os.Symlink("real.txt", filepath.Join(srcDir, "link.txt")), "symlink")

	dst := filepath.Join(t.TempDir(), "copy")

	require.NoError(t, env.Upload(t.Context(), srcDir, dst, invoke.WithSymlinks(invoke.SymlinkFollow)), "Upload")

	linkCopy := filepath.Join(dst, "link.txt")
	assert.Zero(t, fileMode(t, linkCopy)&fs.ModeSymlink, "link.txt is still a symlink under SymlinkFollow")
	assert.Equal(t, "followed", readFile(t, linkCopy), "link.txt content")
}

func TestSymlinkFollowRejectsEscapes(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	outside := writeFixture(t, t.TempDir(), "secret.txt", "outside data", modeDefault)

	srcDir := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(srcDir, "escape.txt")), "symlink")

	err := env.Upload(t.Context(), srcDir, filepath.Join(t.TempDir(), "copy"),
		invoke.WithSymlinks(invoke.SymlinkFollow))
	require.Error(t, err, "following a link out of the transfer root succeeded; it must be rejected")

	assert.ErrorContains(t, err, "escape.txt", "the error does not name the offending link")
}

// TestUploadRefusesASymlinkedDestinationDirectory is the end-to-end shape
// of the containment rule: a link already at the destination, standing
// where the source tree has a directory, must stop the upload rather than
// carry it outside the destination the caller named.
func TestUploadRefusesASymlinkedDestinationDirectory(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	base := t.TempDir()
	srcDir := filepath.Join(base, "src")
	dstDir := filepath.Join(base, "dst")
	outside := filepath.Join(base, "outside")

	require.NoError(t, os.MkdirAll(filepath.Join(srcDir, "sub"), 0o755), "source tree")
	writeFixture(t, filepath.Join(srcDir, "sub"), "payload.txt", "payload", modeDefault)
	require.NoError(t, os.MkdirAll(outside, 0o755), "outside directory")
	require.NoError(t, os.MkdirAll(dstDir, 0o755), "destination")
	require.NoError(t, os.Symlink(outside, filepath.Join(dstDir, "sub")), "symlink")

	err := env.Upload(t.Context(), srcDir, dstDir)
	require.Error(t, err, "an upload through a symlinked destination directory reported success")

	assert.NoFileExists(t, filepath.Join(outside, "payload.txt"),
		"the upload wrote through the link, outside the destination the caller named")
}

func TestSymlinkSkipOmitsLinks(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	srcDir := t.TempDir()
	writeFixture(t, srcDir, "real.txt", "kept", modeDefault)

	require.NoError(t, os.Symlink("real.txt", filepath.Join(srcDir, "link.txt")), "symlink")

	dst := filepath.Join(t.TempDir(), "copy")

	require.NoError(t, env.Upload(t.Context(), srcDir, dst, invoke.WithSymlinks(invoke.SymlinkSkip)), "Upload")

	_, err := os.Lstat(filepath.Join(dst, "link.txt"))
	assert.ErrorIs(t, err, fs.ErrNotExist, "link.txt exists under SymlinkSkip; want omitted")

	assert.Equal(t, "kept", readFile(t, filepath.Join(dst, "real.txt")), "real.txt")
}

func TestSpecialFilesErrorByDefaultAndSkipOnRequest(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	srcDir := t.TempDir()
	writeFixture(t, srcDir, "normal.txt", "normal", modeDefault)

	fifo := filepath.Join(srcDir, "pipe.fifo")
	if err := syscall.Mkfifo(fifo, uint32(modeDefault)); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "copy")

	err := env.Upload(t.Context(), srcDir, dst)
	require.Error(t, err, "Upload with a FIFO succeeded; special files must error by default")

	assert.ErrorContains(t, err, "pipe.fifo", "the error does not name the special file")

	require.NoError(t, env.Upload(t.Context(), srcDir, dst, invoke.WithSkipSpecial()), "Upload with WithSkipSpecial")

	_, err = os.Lstat(filepath.Join(dst, "pipe.fifo"))
	assert.ErrorIs(t, err, fs.ErrNotExist, "FIFO was copied despite WithSkipSpecial")

	assert.Equal(t, "normal", readFile(t, filepath.Join(dst, "normal.txt")), "normal.txt")
}

func TestProgressReportsPerFileTotals(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	srcDir := t.TempDir()
	writeFixture(t, srcDir, "a.txt", strings.Repeat("a", 100), modeDefault)
	writeFixture(t, srcDir, "b.txt", strings.Repeat("b", 500), modeDefault)

	finals := make(map[string]invoke.TransferProgress)

	err := env.Upload(t.Context(), srcDir, filepath.Join(t.TempDir(), "copy"),
		invoke.WithProgress(func(p invoke.TransferProgress) {
			finals[p.Path] = p
		}))
	require.NoError(t, err, "Upload")

	wantSizes := map[string]int64{"a.txt": 100, "b.txt": 500}

	for path, want := range wantSizes {
		final, ok := finals[path]
		if !assert.True(t, ok, "no progress reported for %q", path) {
			continue
		}

		assert.Equal(t, want, final.Total, "%q final progress total", path)
		assert.Equal(t, want, final.Current, "%q final progress current", path)
	}
}

func TestDownloadSharesUploadSemantics(t *testing.T) {
	t.Parallel()

	env := newEnv(t)
	src := writeFixture(t, t.TempDir(), "src.txt", "via download", modeDefault)
	dst := filepath.Join(t.TempDir(), "deep", "parents", "made", "dst.txt")

	require.NoError(t, env.Download(t.Context(), src, dst), "Download")

	assert.Equal(t, "via download", readFile(t, dst), "content")
}
