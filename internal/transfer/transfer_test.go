package transfer_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/internal/transfer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hostileFS is a source filesystem that reports one attacker-chosen name
// for the entries of the transfer root, standing in for a remote side that
// answers a directory listing with whatever it likes.
type hostileFS struct {
	transfer.HostFS

	root string
	name string
}

func (h hostileFS) ReadDir(p string) ([]fs.FileInfo, error) {
	if p != h.root {
		return h.HostFS.ReadDir(p)
	}

	return []fs.FileInfo{stubInfo{name: h.name}}, nil
}

// posixFS is a source filesystem using POSIX path algebra over host
// files, standing in for a remote endpoint whose separator rules differ
// from the destination's.
type posixFS struct {
	transfer.HostFS
}

func (posixFS) Join(elem ...string) string { return path.Join(elem...) }
func (posixFS) Dir(p string) string        { return path.Dir(p) }
func (posixFS) Base(p string) string       { return path.Base(p) }

func (posixFS) Contains(root, p string) bool {
	return p == root || strings.HasPrefix(p, root+"/")
}

// stubInfo is a minimal fs.FileInfo for a regular file.
type stubInfo struct {
	name string
}

func (s stubInfo) Name() string       { return s.name }
func (s stubInfo) Size() int64        { return 0 }
func (s stubInfo) Mode() fs.FileMode  { return 0o644 }
func (s stubInfo) ModTime() time.Time { return time.Time{} }
func (s stubInfo) IsDir() bool        { return false }
func (s stubInfo) Sys() any           { return nil }

// TestWalkRejectsTraversingEntryNames checks a directory entry whose name
// traverses out of the directory it was listed from is refused before it
// is read. The name points at a real file outside the transfer root, so a
// missing check exfiltrates it rather than merely erroring.
func TestWalkRejectsTraversingEntryNames(t *testing.T) {
	t.Parallel()

	// Both roots share a parent, so one ".." step from either side lands
	// on the secret.
	base := t.TempDir()
	secret := filepath.Join(base, "secret.txt")

	require.NoError(t, os.WriteFile(secret, []byte("outside data"), 0o600), "writing fixture")

	srcDir := filepath.Join(base, "src")
	require.NoError(t, os.Mkdir(srcDir, 0o750), "mkdir")

	dstDir := filepath.Join(base, "dst")
	name := ".." + string(filepath.Separator) + "secret.txt"

	err := transfer.Copy(t.Context(), hostileFS{root: srcDir, name: name}, srcDir,
		transfer.HostFS{}, dstDir, invoke.TransferConfig{})
	require.Error(t, err, "Copy accepted an entry name traversing out of the transfer root")

	// The decisive assertion: the outside file must not have been read
	// and rewritten anywhere.
	if _, statErr := os.Stat(filepath.Join(base, "secret.txt.copy")); statErr == nil {
		assert.Fail(t, "the outside file was copied")
	}

	entries, readErr := os.ReadDir(base)
	require.NoError(t, readErr, "reading base")

	for _, entry := range entries {
		assert.Contains(t, []string{"secret.txt", "src", "dst"}, entry.Name(),
			"transfer wrote %q outside the destination root", entry.Name())
	}

	got, readErr := os.ReadFile(filepath.Join(dstDir, "secret.txt"))
	assert.Error(t, readErr, "outside content landed in the destination: %q", got)
}

// TestWalkRejectsDegenerateEntryNames checks names that address a
// directory itself, or nothing at all, are refused rather than acted on.
func TestWalkRejectsDegenerateEntryNames(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"", ".", ".."} {
		t.Run("name="+strconv.Quote(name), func(t *testing.T) {
			t.Parallel()

			srcDir := t.TempDir()
			dstDir := filepath.Join(t.TempDir(), "dst")

			err := transfer.Copy(t.Context(), hostileFS{root: srcDir, name: name}, srcDir,
				transfer.HostFS{}, dstDir, invoke.TransferConfig{})
			require.Error(t, err, "Copy accepted the entry name %q; it must be refused", name)

			assert.True(t,
				strings.Contains(err.Error(), "usable name") || strings.Contains(err.Error(), "escapes"),
				"error %q does not report the entry name as the problem", err)
		})
	}
}

// TestWalkAcceptsNamesLegalOnTheSourceSide checks the containment check
// screens by each side's own path rules rather than by a fixed character
// set: a backslash is an ordinary character in a POSIX filename, and a
// POSIX-to-POSIX transfer must carry it.
func TestWalkAcceptsNamesLegalOnTheSourceSide(t *testing.T) {
	t.Parallel()

	if filepath.Separator != '/' {
		t.Skip("backslash is a separator on this host, so the name is not legal here")
	}

	srcDir := t.TempDir()
	name := `we\ird.txt`

	require.NoError(t, os.WriteFile(filepath.Join(srcDir, name), []byte("payload"), 0o600), "writing fixture")

	dstDir := filepath.Join(t.TempDir(), "dst")

	require.NoError(t,
		transfer.Copy(t.Context(), posixFS{}, srcDir, transfer.HostFS{}, dstDir, invoke.TransferConfig{}),
		"want a legitimate backslash filename to transfer")

	got, err := os.ReadFile(filepath.Join(dstDir, name))
	require.NoError(t, err, "reading transferred file")

	assert.Equal(t, "payload", string(got))
}

// TestWalkRefusesASymlinkedDestinationDirectory pins the containment
// boundary against a link already at the destination. The containment
// check compares names, and a name never leaves the root, so a link
// standing where the tree expects a directory would carry the transfer
// out of the destination while every check still read as contained.
func TestWalkRefusesASymlinkedDestinationDirectory(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	srcDir := filepath.Join(base, "src")
	dstDir := filepath.Join(base, "dst")
	outside := filepath.Join(base, "outside")

	require.NoError(t, os.MkdirAll(filepath.Join(srcDir, "sub"), 0o755), "source tree")
	require.NoError(t,
		os.WriteFile(filepath.Join(srcDir, "sub", "payload.txt"), []byte("payload"), 0o600), "writing fixture")
	require.NoError(t, os.MkdirAll(outside, 0o755), "outside directory")
	require.NoError(t, os.MkdirAll(dstDir, 0o755), "destination")

	// The destination already holds a link where the source has a
	// directory: the shape of the source decides this path, not the caller.
	require.NoError(t, os.Symlink(outside, filepath.Join(dstDir, "sub")), "symlink")

	err := transfer.Copy(t.Context(), transfer.HostFS{}, srcDir, transfer.HostFS{}, dstDir, invoke.TransferConfig{})
	require.Error(t, err, "a transfer through a symlinked destination directory reported success")

	assert.ErrorContains(t, err, "symbolic link", "the error does not say why the transfer stopped")

	assert.NoFileExists(t, filepath.Join(outside, "payload.txt"),
		"the transfer wrote through the link, outside the destination the caller named")
}

// TestCopyFollowsASymlinkedDestinationRoot pins the other half of the
// rule, so the containment fix is not later widened into a regression: the
// root is the one path the caller named, and pointing a stable name at the
// current release directory is the ordinary way to deploy.
func TestCopyFollowsASymlinkedDestinationRoot(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	srcDir := filepath.Join(base, "src")
	release := filepath.Join(base, "release")
	current := filepath.Join(base, "current")

	require.NoError(t, os.MkdirAll(srcDir, 0o755), "source tree")
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "app.txt"), []byte("payload"), 0o600), "writing fixture")
	require.NoError(t, os.MkdirAll(release, 0o755), "release directory")
	require.NoError(t, os.Symlink(release, current), "symlink")

	require.NoError(t,
		transfer.Copy(t.Context(), transfer.HostFS{}, srcDir, transfer.HostFS{}, current, invoke.TransferConfig{}),
		"a transfer to a symlinked root the caller named must be delivered")

	got, err := os.ReadFile(filepath.Join(release, "app.txt"))
	require.NoError(t, err, "reading transferred file")

	assert.Equal(t, "payload", string(got))
}

// TestOverlapGuardResolvesASymlinkedDestination pins the guard against a
// destination that reaches back into the source through a symlink.
//
// The paths as written do not overlap — "link/sub" is not lexically inside
// "real" — but "link" resolves to "real", so the copy would write the tree
// into itself and recurse until the names grow too long. The guard must
// see the resolved destination and refuse before anything is created.
func TestOverlapGuardResolvesASymlinkedDestination(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	srcTree := filepath.Join(base, "real")

	require.NoError(t, os.MkdirAll(srcTree, 0o755), "source tree")
	require.NoError(t, os.WriteFile(filepath.Join(srcTree, "f.txt"), []byte("payload"), 0o600), "writing fixture")
	require.NoError(t, os.Symlink(srcTree, filepath.Join(base, "link")), "symlink")

	err := transfer.Copy(t.Context(), transfer.HostFS{}, srcTree,
		transfer.HostFS{}, filepath.Join(base, "link", "sub"), invoke.TransferConfig{})
	require.Error(t, err, "a copy into a symlink that resolves inside the source reported success")

	assert.ErrorContains(t, err, "inside the source tree",
		"the guard did not recognize the destination as overlapping; it recursed instead")

	// The guard runs before anything is created, so the source stays as it
	// was — no tree copied into itself.
	_, err = os.Stat(filepath.Join(srcTree, "sub"))
	assert.Error(t, err, "the copy created entries inside the source before refusing")
}

// TestOverlapGuardResolvesToTheSamePath pins the case where the
// destination is a different name for the source itself.
func TestOverlapGuardResolvesToTheSamePath(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	srcTree := filepath.Join(base, "real")

	require.NoError(t, os.MkdirAll(srcTree, 0o755), "source tree")
	require.NoError(t, os.Symlink(srcTree, filepath.Join(base, "link")), "symlink")

	err := transfer.Copy(t.Context(), transfer.HostFS{}, srcTree,
		transfer.HostFS{}, filepath.Join(base, "link"), invoke.TransferConfig{})
	require.Error(t, err, "a copy onto a symlink to the source itself reported success")

	assert.ErrorContains(t, err, "resolve to the same path",
		"the guard did not recognize the destination as the source under another name")
}

// TestOverlapGuardAllowsAnUnrelatedSymlinkedDestination guards the other
// side of the rule: resolving must not reject a destination reached
// through a symlink that leads somewhere else entirely — the ordinary
// current-points-at-a-release deploy shape.
func TestOverlapGuardAllowsAnUnrelatedSymlinkedDestination(t *testing.T) {
	t.Parallel()

	base := t.TempDir()

	src := filepath.Join(base, "build")
	require.NoError(t, os.MkdirAll(src, 0o755), "source tree")
	require.NoError(t, os.WriteFile(filepath.Join(src, "app"), []byte("payload"), 0o600), "writing fixture")

	release := filepath.Join(base, "release")
	require.NoError(t, os.MkdirAll(release, 0o755), "release directory")
	require.NoError(t, os.Symlink(release, filepath.Join(base, "current")), "symlink")

	require.NoError(t,
		transfer.Copy(t.Context(), transfer.HostFS{}, src,
			transfer.HostFS{}, filepath.Join(base, "current", "app"), invoke.TransferConfig{}),
		"a copy through a symlink that leads outside the source must be allowed")

	got, err := os.ReadFile(filepath.Join(release, "app", "app"))
	require.NoError(t, err, "reading the transferred file")

	assert.Equal(t, "payload", string(got))
}

// TestCopyRejectsCanceledContext checks the engine refuses before it
// creates anything, including for a source with no entries to check.
func TestCopyRejectsCanceledContext(t *testing.T) {
	t.Parallel()

	srcDir := t.TempDir()
	dstDir := filepath.Join(t.TempDir(), "dst")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	require.Error(t,
		transfer.Copy(ctx, transfer.HostFS{}, srcDir, transfer.HostFS{}, dstDir, invoke.TransferConfig{}),
		"Copy with a canceled context reported success")

	_, err := os.Stat(dstDir)
	assert.Error(t, err, "a canceled Copy created the destination")
}

// sizeCapturingFS is a destination filesystem whose files record the
// reader the copy offers them, standing in for a destination that
// schedules its writes by asking that reader for the source's size —
// which is what pkg/sftp's ReadFrom does.
type sizeCapturingFS struct {
	transfer.HostFS

	offered []io.Reader
}

func (f *sizeCapturingFS) CreateExclusive(p string) (transfer.WriteFile, error) {
	file, err := f.HostFS.CreateExclusive(p)
	if err != nil {
		return nil, err
	}

	return &sizeCapturingFile{WriteFile: file, offered: &f.offered}, nil
}

// sizeCapturingFile records the reader io.Copy offers it, then lets the
// copy proceed.
type sizeCapturingFile struct {
	transfer.WriteFile

	offered *[]io.Reader
}

func (f *sizeCapturingFile) ReadFrom(r io.Reader) (int64, error) {
	*f.offered = append(*f.offered, r)

	return io.Copy(f.WriteFile, r)
}

// TestCopyOffersTheSourceSizeToTheDestination pins the size hint the
// copy hands a destination that schedules by it. The regression this
// guards is invisible any other way: a wrapper reshuffle that hides the
// size breaks no behavior — remote uploads just quietly revert to one
// request per round trip.
func TestCopyOffersTheSourceSizeToTheDestination(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		opts []invoke.TransferOption
	}{
		{name: "bare", opts: nil},
		{name: "with progress", opts: []invoke.TransferOption{
			invoke.WithProgress(func(invoke.TransferProgress) {}),
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Larger than one 32 KiB packet, so a hidden size could not
			// hide behind the small-file path.
			content := strings.Repeat("x", 100_000)

			src := filepath.Join(t.TempDir(), "payload")
			require.NoError(t, os.WriteFile(src, []byte(content), 0o600), "writing fixture")

			dst := &sizeCapturingFS{}

			require.NoError(t,
				transfer.Copy(t.Context(), transfer.HostFS{}, src,
					dst, filepath.Join(t.TempDir(), "delivered"),
					invoke.NewTransferConfig(tc.opts...)))

			require.Len(t, dst.offered, 1, "exactly one file was copied")

			sized, ok := dst.offered[0].(interface{ Size() int64 })
			require.True(t, ok,
				"the reader offered to the destination does not carry the source's size, "+
					"so a scheduling destination would fall back to one request per round trip")
			assert.Equal(t, int64(len(content)), sized.Size(),
				"the offered size must be the source's stat'ed size")
		})
	}
}

// streamingFS is a source filesystem whose files deliver themselves via
// WriteTo, standing in for the transport side (pkg/sftp's File), whose
// own delivery pipelines only when the copy lets it drive.
type streamingFS struct {
	transfer.HostFS

	streamed []io.Writer
}

func (f *streamingFS) Open(p string) (transfer.ReadFile, error) {
	file, err := f.HostFS.Open(p)
	if err != nil {
		return nil, err
	}

	return &streamingFile{ReadFile: file, streamed: &f.streamed}, nil
}

// streamingFile records the writer WriteTo is handed, then delivers.
type streamingFile struct {
	transfer.ReadFile

	streamed *[]io.Writer
}

func (f *streamingFile) WriteTo(w io.Writer) (int64, error) {
	*f.streamed = append(*f.streamed, w)

	return io.Copy(w, f.ReadFile)
}

// TestCopyLetsAStreamingSourceDrive pins that a source advertising
// WriteTo is handed the destination and drives the copy itself — the
// path where pkg/sftp pipelines a download's read requests — and that
// cancellation checks and progress survive the handoff on the writer
// side.
func TestCopyLetsAStreamingSourceDrive(t *testing.T) {
	t.Parallel()

	content := strings.Repeat("y", 100_000)

	streamCopy := func(t *testing.T, opts ...invoke.TransferOption) *streamingFS {
		t.Helper()

		src := filepath.Join(t.TempDir(), "payload")
		require.NoError(t, os.WriteFile(src, []byte(content), 0o600), "writing fixture")

		source := &streamingFS{}
		delivered := filepath.Join(t.TempDir(), "delivered")

		require.NoError(t,
			transfer.Copy(t.Context(), source, src,
				transfer.HostFS{}, delivered,
				invoke.NewTransferConfig(opts...)))

		require.Len(t, source.streamed, 1,
			"a WriterTo source must drive the copy; reading it from the engine "+
				"costs a pipelining transport one request per round trip")

		got, err := os.ReadFile(delivered)
		require.NoError(t, err, "reading the delivered file")
		assert.Equal(t, content, string(got), "the streamed copy must deliver the full content")

		return source
	}

	t.Run("bare", func(t *testing.T) {
		t.Parallel()

		streamCopy(t)
	})

	t.Run("with progress", func(t *testing.T) {
		t.Parallel()

		var events []invoke.TransferProgress

		streamCopy(t, invoke.WithProgress(func(p invoke.TransferProgress) {
			events = append(events, p)
		}))

		require.NotEmpty(t, events, "progress must still be reported when the source drives")

		last := events[len(events)-1]
		assert.Equal(t, int64(len(content)), last.Current, "progress must reach the full size")
		assert.Equal(t, int64(len(content)), last.Total, "progress must carry the stat'ed total")
	})
}

// TestHostFilesDoNotStream pins the other half of the routing: a host
// file must not advertise io.WriterTo. *os.File implements it with a
// generic loop, and honoring that on an upload would take the copy away
// from a destination that schedules its writes — silently trading
// pipelined requests for one write per round trip.
func TestHostFilesDoNotStream(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600), "writing fixture")

	file, err := transfer.HostFS{}.Open(path)
	require.NoError(t, err)

	defer func() { _ = file.Close() }()

	_, streams := file.(io.WriterTo)
	assert.False(t, streams,
		"HostFS.Open returned a file advertising io.WriterTo; uploads would let the "+
			"host side drive the copy and defeat the destination's request pipelining")
}

// rendezvousFS proves two file copies are inside their writes at the
// same moment: each destination file's first Write reports arrival and
// blocks until released — a rendezvous a copier moving one file at a
// time can never complete.
type rendezvousFS struct {
	transfer.HostFS

	arrivals chan struct{}
	release  chan struct{}

	// cutOff, when set, is what a released write returns instead of
	// proceeding: the copy was interrupted, and this is the error the
	// interruption surfaced as.
	cutOff error
}

func (f *rendezvousFS) CreateExclusive(p string) (transfer.WriteFile, error) {
	file, err := f.HostFS.CreateExclusive(p)
	if err != nil {
		return nil, err
	}

	return &rendezvousFile{WriteFile: file, fs: f}, nil
}

type rendezvousFile struct {
	transfer.WriteFile

	fs   *rendezvousFS
	held bool
}

func (f *rendezvousFile) Write(p []byte) (int, error) {
	if !f.held {
		f.held = true
		f.fs.arrivals <- struct{}{}

		<-f.fs.release

		if f.fs.cutOff != nil {
			return 0, f.fs.cutOff
		}
	}

	return f.WriteFile.Write(p)
}

// TestTreeCopyRunsFilesConcurrently pins that WithConcurrency actually
// engages the worker pool: two files must be mid-write at once.
func TestTreeCopyRunsFilesConcurrently(t *testing.T) {
	t.Parallel()

	srcDir := t.TempDir()
	for i := range 4 {
		name := filepath.Join(srcDir, "f"+strconv.Itoa(i))
		require.NoError(t, os.WriteFile(name, []byte(strings.Repeat("x", 64)), 0o600), "writing fixture")
	}

	dst := &rendezvousFS{arrivals: make(chan struct{}, 4), release: make(chan struct{})}
	done := make(chan error, 1)

	go func() {
		done <- transfer.Copy(t.Context(), transfer.HostFS{}, srcDir,
			dst, filepath.Join(t.TempDir(), "out"),
			invoke.NewTransferConfig(invoke.WithConcurrency(2)))
	}()

	for arrived := 0; arrived < 2; {
		select {
		case <-dst.arrivals:
			arrived++

		case err := <-done:
			require.Failf(t, "the copy finished before two files were in flight", "copy returned early: %v", err)

		case <-time.After(5 * time.Second):
			close(dst.release)
			require.FailNow(t, "two files never wrote concurrently; the worker pool did not engage")
		}
	}

	close(dst.release)
	require.NoError(t, <-done, "the concurrent copy must still deliver")
}

// failingOpenFS fails to open one specific source file, standing in for
// any mid-tree per-file failure.
type failingOpenFS struct {
	transfer.HostFS

	failPath string
}

var errPlanted = errors.New("planted per-file failure")

func (f failingOpenFS) Open(p string) (transfer.ReadFile, error) {
	if p == f.failPath {
		return nil, errPlanted
	}

	return f.HostFS.Open(p)
}

// TestTreeCopyReportsTheCauseNotTheCancellation pins the pool's error
// discipline: the planted failure is what the transfer reports — never
// the context error the other workers were stopped with — and no
// half-written temp files survive at the destination.
func TestTreeCopyReportsTheCauseNotTheCancellation(t *testing.T) {
	t.Parallel()

	srcDir := t.TempDir()
	for i := range 12 {
		name := filepath.Join(srcDir, "f"+strconv.Itoa(i))
		require.NoError(t, os.WriteFile(name, []byte(strings.Repeat("y", 4096)), 0o600), "writing fixture")
	}

	src := failingOpenFS{failPath: filepath.Join(srcDir, "f6")}
	dstDir := filepath.Join(t.TempDir(), "out")

	err := transfer.Copy(t.Context(), src, srcDir, transfer.HostFS{}, dstDir,
		invoke.NewTransferConfig(invoke.WithConcurrency(4)))

	require.ErrorIs(t, err, errPlanted,
		"the transfer must report the failure's cause, not the cancellation that spread it")
	require.NotErrorIs(t, err, context.Canceled,
		"a cancellation echo must never outrank the failure that caused it")

	entries, readErr := os.ReadDir(dstDir)
	require.NoError(t, readErr, "reading the destination")

	for _, entry := range entries {
		assert.NotContains(t, entry.Name(), ".invoke-",
			"a canceled in-flight copy left its temp file behind")
	}
}

// TestTreeCopyRestoresModesAfterTheCopies pins the ordering between the
// worker pool and the deferred directory-mode pass: a read-only source
// directory must get its mode back only after every file inside it has
// landed, or the copies would be writing into a directory they cannot
// create files in.
func TestTreeCopyRestoresModesAfterTheCopies(t *testing.T) {
	t.Parallel()

	srcDir := t.TempDir()
	sub := filepath.Join(srcDir, "locked")
	require.NoError(t, os.Mkdir(sub, 0o755), "mkdir")

	for i := range 6 {
		name := filepath.Join(sub, "f"+strconv.Itoa(i))
		require.NoError(t, os.WriteFile(name, []byte("payload"), 0o600), "writing fixture")
	}

	require.NoError(t, os.Chmod(sub, 0o500), "locking the source directory")
	t.Cleanup(func() { _ = os.Chmod(sub, 0o700) })

	dstDir := filepath.Join(t.TempDir(), "out")
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dstDir, "locked"), 0o700) })

	require.NoError(t,
		transfer.Copy(t.Context(), transfer.HostFS{}, srcDir, transfer.HostFS{}, dstDir,
			invoke.NewTransferConfig(invoke.WithConcurrency(4))),
		"a read-only directory's files must land before its mode is restored")

	info, err := os.Stat(filepath.Join(dstDir, "locked"))
	require.NoError(t, err, "stat the delivered directory")
	assert.Equal(t, fs.FileMode(0o500), info.Mode().Perm(), "the source directory's mode must be restored")

	for i := range 6 {
		got, err := os.ReadFile(filepath.Join(dstDir, "locked", "f"+strconv.Itoa(i)))
		require.NoErrorf(t, err, "reading delivered file %d", i)
		assert.Equal(t, "payload", string(got))
	}
}

// hintingFS is a destination that advertises a preferred concurrency, as
// the SFTP side does.
type hintingFS struct {
	*rendezvousFS
}

func (hintingFS) CopyConcurrency() int { return 2 }

// TestTreeCopyHonorsTheSideHint pins that a side's advertised
// concurrency engages the pool with no option from the caller. The
// rendezvous can only complete when two files are mid-write at once, so
// a hint that stopped being consulted fails here instead of quietly
// costing every remote tree transfer its overlap.
func TestTreeCopyHonorsTheSideHint(t *testing.T) {
	t.Parallel()

	srcDir := t.TempDir()
	for i := range 4 {
		name := filepath.Join(srcDir, "f"+strconv.Itoa(i))
		require.NoError(t, os.WriteFile(name, []byte(strings.Repeat("x", 64)), 0o600), "writing fixture")
	}

	dst := hintingFS{&rendezvousFS{arrivals: make(chan struct{}, 4), release: make(chan struct{})}}
	done := make(chan error, 1)

	go func() {
		done <- transfer.Copy(t.Context(), transfer.HostFS{}, srcDir,
			dst, filepath.Join(t.TempDir(), "out"), invoke.TransferConfig{})
	}()

	for arrived := 0; arrived < 2; {
		select {
		case <-dst.arrivals:
			arrived++

		case err := <-done:
			require.Failf(t, "the copy finished before two files were in flight", "copy returned early: %v", err)

		case <-time.After(5 * time.Second):
			close(dst.release)
			require.FailNow(t, "two files never wrote concurrently; the side's hint was not honored")
		}
	}

	close(dst.release)
	require.NoError(t, <-done, "the hinted copy must still deliver")
}

// lateHostileFS lists the transfer root's real entries and then one
// attacker-chosen name that sorts after them, so the walk fails only
// after it has dispatched a real copy — and not before that copy is
// provably mid-write: the containment check that rejects the hostile
// name waits on gate first.
type lateHostileFS struct {
	transfer.HostFS

	root string
	gate <-chan struct{}
}

func (h lateHostileFS) ReadDir(p string) ([]fs.FileInfo, error) {
	entries, err := h.HostFS.ReadDir(p)
	if err != nil || p != h.root {
		return entries, err
	}

	return append(entries, stubInfo{name: "zz/../../escape"}), nil
}

func (h lateHostileFS) Contains(root, p string) bool {
	if strings.HasSuffix(p, "escape") {
		<-h.gate
	}

	return h.HostFS.Contains(root, p)
}

// TestTreeCopyReportsTheWalkFailureOverInFlightCancellations pins the
// pool's error discipline from the other side: when the walk itself
// fails while a copy is mid-write, the cancellation that stops the copy
// echoes back as a context error, and that echo must never outrank the
// walk's own failure in what the transfer reports.
//
// The in-flight write is cut off with the context error itself rather
// than left to meet the canceled context on its next read, so the echo
// is produced every run instead of only when the cancellation wins the
// race against the copy's last read.
func TestTreeCopyReportsTheWalkFailureOverInFlightCancellations(t *testing.T) {
	t.Parallel()

	srcDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "f0"), []byte(strings.Repeat("x", 64)), 0o600),
		"writing fixture")

	dst := &rendezvousFS{
		arrivals: make(chan struct{}, 2),
		release:  make(chan struct{}),
		cutOff:   context.Canceled,
	}
	gate := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		done <- transfer.Copy(t.Context(), lateHostileFS{root: srcDir, gate: gate}, srcDir,
			dst, filepath.Join(t.TempDir(), "out"),
			invoke.NewTransferConfig(invoke.WithConcurrency(2)))
	}()

	select {
	case <-dst.arrivals:
	case <-time.After(5 * time.Second):
		close(gate)
		close(dst.release)
		require.FailNow(t, "the real file never started copying")
	}

	// f0 is mid-write. Let the walk meet the hostile entry and fail,
	// then release the write so the in-flight copy unwinds with the
	// cancellation that failure triggered.
	close(gate)
	close(dst.release)

	err := <-done
	require.Error(t, err, "a walk that met an escaping entry reported success")
	assert.ErrorContains(t, err, "escapes", "the transfer must report the walk's own failure")
	assert.NotErrorIs(t, err, context.Canceled,
		"a cancellation echo from the in-flight copy outranked the failure that caused it")
}
