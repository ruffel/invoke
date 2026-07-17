package local_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/ruffel/invoke"
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
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("writing fixture %q: %v", path, err)
	}

	// WriteFile is umask-subject; pin the intended mode explicitly.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod fixture %q: %v", path, err)
	}

	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %q: %v", path, err)
	}

	return string(content)
}

func fileMode(t *testing.T, path string) fs.FileMode {
	t.Helper()

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %q: %v", path, err)
	}

	return info.Mode().Perm()
}

func TestUploadRoundTripPreservesContentAndMode(t *testing.T) {
	t.Parallel()

	env := newEnv(t)
	src := writeFixture(t, t.TempDir(), "src.txt", "payload", modeGrouped)
	dst := filepath.Join(t.TempDir(), "dst.txt")

	if err := env.Upload(t.Context(), src, dst); err != nil {
		t.Fatalf("Upload = %v", err)
	}

	if got := readFile(t, dst); got != "payload" {
		t.Errorf("content = %q, want %q", got, "payload")
	}

	// 0640 survives even though a typical umask would clip a fresh
	// create to 0620: modes are applied by chmod, not at open time.
	if got := fileMode(t, dst); got != modeGrouped {
		t.Errorf("mode = %v, want %v", got, modeGrouped)
	}
}

func TestWithModeAppliesOnOverwrite(t *testing.T) {
	t.Parallel()

	env := newEnv(t)
	src := writeFixture(t, t.TempDir(), "src.txt", "new content", modeDefault)

	dstDir := t.TempDir()
	dst := writeFixture(t, dstDir, "dst.txt", "old content", modeDefault)

	if err := env.Upload(t.Context(), src, dst, invoke.WithMode(modePrivate)); err != nil {
		t.Fatalf("Upload = %v", err)
	}

	if got := readFile(t, dst); got != "new content" {
		t.Errorf("content = %q, want overwritten", got)
	}

	if got := fileMode(t, dst); got != modePrivate {
		t.Errorf("mode after overwrite = %v, want %v (WithMode must beat the pre-existing mode)", got, modePrivate)
	}
}

func TestSamePathIsRejectedAndDataSurvives(t *testing.T) {
	t.Parallel()

	env := newEnv(t)
	src := writeFixture(t, t.TempDir(), "precious.txt", "PRECIOUS DATA", modeDefault)

	if err := env.Upload(t.Context(), src, src); err == nil {
		t.Fatal("Upload(p, p) succeeded; it must be rejected")
	}

	if got := readFile(t, src); got != "PRECIOUS DATA" {
		t.Fatalf("source content = %q after rejected transfer; data was destroyed", got)
	}
}

func TestDestinationInsideSourceIsRejected(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	srcDir := t.TempDir()
	writeFixture(t, srcDir, "a.txt", "a", modeDefault)

	if err := env.Upload(t.Context(), srcDir, filepath.Join(srcDir, "copy")); err == nil {
		t.Fatal("copying a directory into its own subtree succeeded; it must be rejected")
	}
}

func TestFailedTransferLeavesDestinationIntact(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	srcDir := t.TempDir()
	src := writeFixture(t, srcDir, "src.txt", "unreadable", modeDefault)

	if err := os.Chmod(src, 0); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	t.Cleanup(func() { _ = os.Chmod(src, modeDefault) })

	dstDir := t.TempDir()
	dst := writeFixture(t, dstDir, "dst.txt", "precious destination", modeDefault)

	if err := env.Upload(t.Context(), src, dst); err == nil {
		t.Fatal("Upload of unreadable source succeeded, want error")
	}

	if got := readFile(t, dst); got != "precious destination" {
		t.Errorf("destination = %q after failed transfer; atomicity was violated", got)
	}
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
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Upload under cancellation = %v, want context.Canceled", err)
	}

	if got := readFile(t, dst); got != "precious destination" {
		t.Errorf("destination = %q after canceled transfer; atomicity was violated", got)
	}

	leftovers, err := filepath.Glob(filepath.Join(dstDir, ".invoke-*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	if len(leftovers) != 0 {
		t.Errorf("temporary files left behind: %v", leftovers)
	}
}

func TestTreeRoundTrip(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	srcDir := t.TempDir()
	writeFixture(t, srcDir, "top.txt", "top", modeDefault)

	nested := filepath.Join(srcDir, "nested")
	if err := os.Mkdir(nested, modeTreeDir); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := os.Chmod(nested, modeTreeDir); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	writeFixture(t, nested, "deep.txt", "deep", modeGrouped)

	dst := filepath.Join(t.TempDir(), "into", "tree")

	if err := env.Upload(t.Context(), srcDir, dst); err != nil {
		t.Fatalf("Upload = %v", err)
	}

	if got := readFile(t, filepath.Join(dst, "top.txt")); got != "top" {
		t.Errorf("top.txt = %q", got)
	}

	if got := readFile(t, filepath.Join(dst, "nested", "deep.txt")); got != "deep" {
		t.Errorf("nested/deep.txt = %q", got)
	}

	if got := fileMode(t, filepath.Join(dst, "nested")); got != modeTreeDir {
		t.Errorf("nested dir mode = %v, want %v", got, modeTreeDir)
	}

	if got := fileMode(t, filepath.Join(dst, "nested", "deep.txt")); got != modeGrouped {
		t.Errorf("deep.txt mode = %v, want %v", got, modeGrouped)
	}
}

func TestReadOnlySourceDirectoryCopies(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	srcDir := t.TempDir()
	locked := filepath.Join(srcDir, "locked")

	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	writeFixture(t, locked, "inside.txt", "inside", modeDefault)

	if err := os.Chmod(locked, modeRestricted); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	dst := filepath.Join(t.TempDir(), "copy")

	// The copy deliberately ends read-only too; reopen it for TempDir
	// cleanup.
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dst, "locked"), 0o755) })

	if err := env.Upload(t.Context(), srcDir, dst); err != nil {
		t.Fatalf("Upload of read-only dir tree = %v", err)
	}

	if got := readFile(t, filepath.Join(dst, "locked", "inside.txt")); got != "inside" {
		t.Errorf("inside.txt = %q", got)
	}

	if got := fileMode(t, filepath.Join(dst, "locked")); got != modeRestricted {
		t.Errorf("locked dir mode = %v, want %v", got, modeRestricted)
	}
}

func TestSymlinksArePreservedByDefault(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	srcDir := t.TempDir()
	writeFixture(t, srcDir, "real.txt", "real", modeDefault)

	if err := os.Symlink("real.txt", filepath.Join(srcDir, "link.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if err := os.Symlink("gone-target", filepath.Join(srcDir, "dangling.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "copy")

	if err := env.Upload(t.Context(), srcDir, dst); err != nil {
		t.Fatalf("Upload = %v", err)
	}

	if target, err := os.Readlink(filepath.Join(dst, "link.txt")); err != nil || target != "real.txt" {
		t.Errorf("link.txt = (%q, %v), want preserved link to real.txt", target, err)
	}

	if target, err := os.Readlink(filepath.Join(dst, "dangling.txt")); err != nil || target != "gone-target" {
		t.Errorf("dangling.txt = (%q, %v), want the dangling link preserved as-is", target, err)
	}
}

func TestSymlinkFollowCopiesContent(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	srcDir := t.TempDir()
	writeFixture(t, srcDir, "real.txt", "followed", modeDefault)

	if err := os.Symlink("real.txt", filepath.Join(srcDir, "link.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "copy")

	if err := env.Upload(t.Context(), srcDir, dst, invoke.WithSymlinks(invoke.SymlinkFollow)); err != nil {
		t.Fatalf("Upload = %v", err)
	}

	linkCopy := filepath.Join(dst, "link.txt")
	if mode := fileMode(t, linkCopy); mode&fs.ModeSymlink != 0 {
		t.Errorf("link.txt is still a symlink under SymlinkFollow")
	}

	if got := readFile(t, linkCopy); got != "followed" {
		t.Errorf("link.txt content = %q, want %q", got, "followed")
	}
}

func TestSymlinkFollowRejectsEscapes(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	outside := writeFixture(t, t.TempDir(), "secret.txt", "outside data", modeDefault)

	srcDir := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(srcDir, "escape.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	err := env.Upload(t.Context(), srcDir, filepath.Join(t.TempDir(), "copy"),
		invoke.WithSymlinks(invoke.SymlinkFollow))
	if err == nil {
		t.Fatal("following a link out of the transfer root succeeded; it must be rejected")
	}

	if !strings.Contains(err.Error(), "escape.txt") {
		t.Errorf("error %q does not name the offending link", err)
	}
}

func TestSymlinkSkipOmitsLinks(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	srcDir := t.TempDir()
	writeFixture(t, srcDir, "real.txt", "kept", modeDefault)

	if err := os.Symlink("real.txt", filepath.Join(srcDir, "link.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "copy")

	if err := env.Upload(t.Context(), srcDir, dst, invoke.WithSymlinks(invoke.SymlinkSkip)); err != nil {
		t.Fatalf("Upload = %v", err)
	}

	if _, err := os.Lstat(filepath.Join(dst, "link.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("link.txt exists under SymlinkSkip; want omitted")
	}

	if got := readFile(t, filepath.Join(dst, "real.txt")); got != "kept" {
		t.Errorf("real.txt = %q", got)
	}
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
	if err == nil {
		t.Fatal("Upload with a FIFO succeeded; special files must error by default")
	}

	if !strings.Contains(err.Error(), "pipe.fifo") {
		t.Errorf("error %q does not name the special file", err)
	}

	if err := env.Upload(t.Context(), srcDir, dst, invoke.WithSkipSpecial()); err != nil {
		t.Fatalf("Upload with WithSkipSpecial = %v", err)
	}

	if _, err := os.Lstat(filepath.Join(dst, "pipe.fifo")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("FIFO was copied despite WithSkipSpecial")
	}

	if got := readFile(t, filepath.Join(dst, "normal.txt")); got != "normal" {
		t.Errorf("normal.txt = %q", got)
	}
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
	if err != nil {
		t.Fatalf("Upload = %v", err)
	}

	wantSizes := map[string]int64{"a.txt": 100, "b.txt": 500}

	for path, want := range wantSizes {
		final, ok := finals[path]
		if !ok {
			t.Errorf("no progress reported for %q", path)

			continue
		}

		if final.Total != want || final.Current != want {
			t.Errorf("%q final progress = %d/%d, want %d/%d", path, final.Current, final.Total, want, want)
		}
	}
}

func TestDownloadSharesUploadSemantics(t *testing.T) {
	t.Parallel()

	env := newEnv(t)
	src := writeFixture(t, t.TempDir(), "src.txt", "via download", modeDefault)
	dst := filepath.Join(t.TempDir(), "deep", "parents", "made", "dst.txt")

	if err := env.Download(t.Context(), src, dst); err != nil {
		t.Fatalf("Download = %v", err)
	}

	if got := readFile(t, dst); got != "via download" {
		t.Errorf("content = %q", got)
	}
}
