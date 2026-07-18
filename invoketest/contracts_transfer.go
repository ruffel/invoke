package invoketest

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ruffel/invoke"
)

const (
	fixtureMode  = fs.FileMode(0o640)
	overrideMode = fs.FileMode(0o600)

	// bigFileBytes is large enough that a canceled transfer is provably
	// mid-flight when its progress callback fires.
	bigFileBytes = 1 << 20
)

// binaryTail returns bytes appended after the 0-255 ramp in the
// binary-fidelity fixture: a NUL, a high byte, a newline, and a trailing
// NUL.
func binaryTail() []byte {
	return []byte{0x00, 0xFF, '\n', 0x00}
}

func transferContracts() []TestCase {
	return []TestCase{
		transferRoundTrip(),
		transferBinaryContentSurvives(),
		transferModeOverride(),
		transferFailurePreservesDestination(),
		transferCancelPreservesDestination(),
		transferDownloadCancelPreservesDestination(),
		transferTreeCreatesParents(),
		transferEmptyFilesAndDirs(),
		transferSymlinksPreserve(),
		transferSymlinkFollowCopiesContent(),
		transferFollowRejectsEscapes(),
		transferSpecialFiles(),
		transferProgressTotals(),
		transferCanceledBeforeStartDoesNothing(),
	}
}

func transferCanceledBeforeStartDoesNothing() TestCase {
	return TestCase{
		Category:    CategoryTransfer,
		Name:        "canceled-before-start-does-nothing",
		Description: "A transfer whose context is already canceled fails without creating anything, even for an empty source",
		Run: func(t T, env invoke.Environment) {
			// An empty directory is the case that slips through a
			// per-entry cancellation check: there are no entries.
			srcDir := t.TempDir()

			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			remote := "/tmp/invoke-xfer-" + token(t)
			defer cleanupTargetPath(t, env, remote)

			if err := env.Upload(ctx, srcDir, remote); !errors.Is(err, context.Canceled) {
				t.Errorf("Upload with a canceled context = %v, want an error matching context.Canceled", err)
			}

			if targetProbe(t, env, "test -e "+shellQuote(remote)) {
				t.Errorf("a canceled transfer created %q; it must not touch the destination", remote)
			}
		},
	}
}

func transferBinaryContentSurvives() TestCase {
	return TestCase{
		Category:    CategoryTransfer,
		Name:        "binary-content-survives",
		Description: "Arbitrary bytes, including NUL and high bytes, round-trip through a transfer unchanged",
		Run: func(t T, env invoke.Environment) {
			// A spread of bytes that trips text-mode or C-string
			// assumptions: a full 0-255 ramp, then a NUL-bracketed tail.
			const rampSize = 256

			payload := make([]byte, 0, rampSize+len(binaryTail()))
			for b := range rampSize {
				payload = append(payload, byte(b))
			}

			payload = append(payload, binaryTail()...)

			src := filepath.Join(t.TempDir(), "blob.bin")
			if err := os.WriteFile(src, payload, 0o600); err != nil {
				failf(t, "writing binary fixture: %v", err)
			}

			remote := "/tmp/invoke-xfer-" + token(t)
			defer cleanupTargetPath(t, env, remote)

			if err := env.Upload(t.Context(), src, remote); err != nil {
				failf(t, "Upload = %v", err)
			}

			back := filepath.Join(t.TempDir(), "back.bin")
			if err := env.Download(t.Context(), remote, back); err != nil {
				failf(t, "Download = %v", err)
			}

			got, err := os.ReadFile(back)
			if err != nil {
				failf(t, "reading round-tripped blob: %v", err)
			}

			if !bytes.Equal(got, payload) {
				t.Errorf("binary content changed in transit: got %d bytes, want %d", len(got), len(payload))
			}
		},
	}
}

func transferDownloadCancelPreservesDestination() TestCase {
	return TestCase{
		Category:    CategoryTransfer,
		Name:        "download-cancel-preserves-destination",
		Description: "A download canceled mid-flight errors and leaves an existing local destination intact",
		Run: func(t T, env invoke.Environment) {
			// Seed a large file on the target, and a precious local
			// destination the canceled download must not corrupt.
			srcDir := t.TempDir()
			big := writeHostFixture(t, srcDir, "big.bin", strings.Repeat("y", bigFileBytes))
			remote := "/tmp/invoke-xfer-" + token(t)

			defer cleanupTargetPath(t, env, remote)

			if err := env.Upload(t.Context(), big, remote); err != nil {
				failf(t, "seeding Upload = %v", err)
			}

			dstDir := t.TempDir()
			dst := writeHostFixture(t, dstDir, "dst.bin", "precious local destination")

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			err := env.Download(ctx, remote, dst, invoke.WithProgress(func(_ invoke.TransferProgress) {
				cancel()
			}))
			if err == nil {
				failf(t, "canceled Download reported success")
			}

			if !errors.Is(err, context.Canceled) {
				t.Errorf("canceled Download = %v, want an error matching context.Canceled", err)
			}

			if got := readHostFile(t, dst); got != "precious local destination" {
				t.Errorf("local destination = %q after a canceled download; atomicity was violated", got)
			}
		},
	}
}

func transferEmptyFilesAndDirs() TestCase {
	return TestCase{
		Category:    CategoryTransfer,
		Name:        "empty-files-and-dirs",
		Description: "Zero-byte files and empty directories survive a directory transfer",
		Run: func(t T, env invoke.Environment) {
			srcDir := t.TempDir()
			writeHostFixture(t, srcDir, "empty.txt", "")

			emptyDir := filepath.Join(srcDir, "hollow")
			if err := os.Mkdir(emptyDir, 0o750); err != nil {
				failf(t, "mkdir: %v", err)
			}

			remote := "/tmp/invoke-xfer-" + token(t)
			defer cleanupTargetPath(t, env, remote)

			if err := env.Upload(t.Context(), srcDir, remote); err != nil {
				failf(t, "Upload = %v", err)
			}

			if !targetProbe(t, env, "test -d "+shellQuote(remote+"/hollow")) {
				t.Errorf("empty directory did not survive the transfer")
			}

			local := downloadBack(t, env, remote)
			if got := readHostFile(t, filepath.Join(local, "empty.txt")); got != "" {
				t.Errorf("empty.txt round-tripped as %q, want empty", got)
			}
		},
	}
}

func transferSymlinkFollowCopiesContent() TestCase {
	return TestCase{
		Category:    CategoryTransfer,
		Name:        "symlink-follow-copies-content",
		Description: "SymlinkFollow replaces an in-root link with its target's content, not a link and not nothing",
		Gate: func(caps invoke.Capabilities) (bool, string) {
			return caps.SymlinkPreserve, "target does not declare symlink handling"
		},
		Run: func(t T, env invoke.Environment) {
			srcDir := t.TempDir()
			writeHostFixture(t, srcDir, "real.txt", "followed content")

			if err := os.Symlink("real.txt", filepath.Join(srcDir, "link.txt")); err != nil {
				failf(t, "symlink: %v", err)
			}

			remote := "/tmp/invoke-xfer-" + token(t)
			defer cleanupTargetPath(t, env, remote)

			if err := env.Upload(t.Context(), srcDir, remote, invoke.WithSymlinks(invoke.SymlinkFollow)); err != nil {
				failf(t, "Upload = %v", err)
			}

			// On the target, the link path must be a regular file whose
			// content matches the target — not a symlink, not missing.
			linkPath := shellQuote(remote + "/link.txt")
			if !targetProbe(t, env, "test -f "+linkPath+" && test ! -L "+linkPath) {
				t.Errorf("followed link is not a regular file on the target")
			}

			local := downloadBack(t, env, remote)
			if got := readHostFile(t, filepath.Join(local, "link.txt")); got != "followed content" {
				t.Errorf("followed link content = %q, want the target's content", got)
			}
		},
	}
}

// writeHostFixture creates a host-side file with content at fixtureMode,
// applied explicitly so umask cannot interfere.
func writeHostFixture(t T, dir, name, content string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), fixtureMode); err != nil {
		failf(t, "writing fixture %q: %v", path, err)
	}

	if err := os.Chmod(path, fixtureMode); err != nil {
		failf(t, "chmod fixture %q: %v", path, err)
	}

	return path
}

// downloadBack fetches a target path into a fresh host location and
// returns that location.
func downloadBack(t T, env invoke.Environment, remote string) string {
	t.Helper()

	local := filepath.Join(t.TempDir(), "downloaded")
	if err := env.Download(t.Context(), remote, local); err != nil {
		failf(t, "Download(%q) = %v", remote, err)
	}

	return local
}

// readHostFile reads a host-side file or fails the contract.
func readHostFile(t T, path string) string {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		failf(t, "reading %q: %v", path, err)
	}

	return string(content)
}

// probeTargetMode checks a target path's permission bits via the target's
// own tools, so upload modes are verified independently of download
// behavior.
func probeTargetMode(t T, env invoke.Environment, path string, mode fs.FileMode) bool {
	t.Helper()

	probe := "test -n \"$(find " + shellQuote(path) + " -maxdepth 0 -perm " + octal(mode) + ")\""

	return targetProbe(t, env, probe)
}

// octal renders a file mode as a four-digit octal literal for find -perm.
func octal(mode fs.FileMode) string {
	const octalBase = 8

	s := "0000" + strconv.FormatInt(int64(mode.Perm()), octalBase)

	return s[len(s)-4:]
}

func transferRoundTrip() TestCase {
	return TestCase{
		Category:    CategoryTransfer,
		Name:        "roundtrip-preserves-content-and-mode",
		Description: "An uploaded file arrives intact with its mode preserved, verified on the target and by download",
		Run: func(t T, env invoke.Environment) {
			src := writeHostFixture(t, t.TempDir(), "src.txt", "payload")
			remote := "/tmp/invoke-xfer-" + token(t)

			defer cleanupTargetPath(t, env, remote)

			if err := env.Upload(t.Context(), src, remote); err != nil {
				failf(t, "Upload = %v", err)
			}

			if !probeTargetMode(t, env, remote, fixtureMode) {
				t.Errorf("target mode is not %v; upload must preserve the source mode umask-proof", fixtureMode)
			}

			local := downloadBack(t, env, remote)
			if got := readHostFile(t, local); got != "payload" {
				t.Errorf("round-tripped content = %q, want %q", got, "payload")
			}
		},
	}
}

func transferModeOverride() TestCase {
	return TestCase{
		Category:    CategoryTransfer,
		Name:        "mode-override-applies-on-overwrite",
		Description: "WithMode forces the destination mode even when the destination already exists",
		Run: func(t T, env invoke.Environment) {
			dir := t.TempDir()
			first := writeHostFixture(t, dir, "first.txt", "old content")
			second := writeHostFixture(t, dir, "second.txt", "new content")
			remote := "/tmp/invoke-xfer-" + token(t)

			defer cleanupTargetPath(t, env, remote)

			if err := env.Upload(t.Context(), first, remote); err != nil {
				failf(t, "first Upload = %v", err)
			}

			if err := env.Upload(t.Context(), second, remote, invoke.WithMode(overrideMode)); err != nil {
				failf(t, "second Upload = %v", err)
			}

			if !probeTargetMode(t, env, remote, overrideMode) {
				t.Errorf("target mode is not %v; WithMode must apply on overwrite", overrideMode)
			}

			if got := readHostFile(t, downloadBack(t, env, remote)); got != "new content" {
				t.Errorf("content = %q, want the overwriting upload's content", got)
			}
		},
	}
}

func transferFailurePreservesDestination() TestCase {
	return TestCase{
		Category:    CategoryTransfer,
		Name:        "failure-preserves-destination",
		Description: "A failed transfer leaves an existing destination byte-for-byte intact",
		Run: func(t T, env invoke.Environment) {
			dir := t.TempDir()
			good := writeHostFixture(t, dir, "good.txt", "precious destination")
			unreadable := writeHostFixture(t, dir, "unreadable.txt", "never seen")
			remote := "/tmp/invoke-xfer-" + token(t)

			defer cleanupTargetPath(t, env, remote)

			if err := env.Upload(t.Context(), good, remote); err != nil {
				failf(t, "seeding Upload = %v", err)
			}

			if err := os.Chmod(unreadable, 0); err != nil {
				failf(t, "chmod: %v", err)
			}

			if err := env.Upload(t.Context(), unreadable, remote); err == nil {
				failf(t, "uploading an unreadable source succeeded, want an error")
			}

			if got := readHostFile(t, downloadBack(t, env, remote)); got != "precious destination" {
				t.Errorf("destination = %q after a failed transfer; atomicity was violated", got)
			}
		},
	}
}

func transferCancelPreservesDestination() TestCase {
	return TestCase{
		Category:    CategoryTransfer,
		Name:        "cancel-preserves-destination",
		Description: "A transfer canceled mid-flight errors with ctx.Err() and leaves the destination intact",
		Run: func(t T, env invoke.Environment) {
			dir := t.TempDir()
			good := writeHostFixture(t, dir, "good.txt", "precious destination")
			big := writeHostFixture(t, dir, "big.bin", strings.Repeat("x", bigFileBytes))
			remote := "/tmp/invoke-xfer-" + token(t)

			defer cleanupTargetPath(t, env, remote)

			if err := env.Upload(t.Context(), good, remote); err != nil {
				failf(t, "seeding Upload = %v", err)
			}

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			err := env.Upload(ctx, big, remote, invoke.WithProgress(func(_ invoke.TransferProgress) {
				cancel()
			}))
			if err == nil {
				failf(t, "canceled Upload reported success")
			}

			if !errors.Is(err, context.Canceled) {
				t.Errorf("canceled Upload = %v, want an error matching context.Canceled", err)
			}

			if got := readHostFile(t, downloadBack(t, env, remote)); got != "precious destination" {
				t.Errorf("destination = %q after a canceled transfer; atomicity was violated", got)
			}
		},
	}
}

func transferTreeCreatesParents() TestCase {
	return TestCase{
		Category:    CategoryTransfer,
		Name:        "tree-roundtrip-creates-parents",
		Description: "Directory trees transfer whole, creating missing destination parents",
		Run: func(t T, env invoke.Environment) {
			srcDir := t.TempDir()
			writeHostFixture(t, srcDir, "top.txt", "top")

			nested := filepath.Join(srcDir, "nested")
			if err := os.Mkdir(nested, 0o700); err != nil {
				failf(t, "mkdir: %v", err)
			}

			// os.Mkdir is umask-subject; pin the intended dir mode.
			if err := os.Chmod(nested, 0o700); err != nil {
				failf(t, "chmod: %v", err)
			}

			writeHostFixture(t, nested, "deep.txt", "deep")

			base := "/tmp/invoke-xfer-" + token(t)
			remote := base + "/made/parents/tree"

			defer cleanupTargetPath(t, env, base)

			if err := env.Upload(t.Context(), srcDir, remote); err != nil {
				failf(t, "Upload = %v", err)
			}

			// The nested directory's own mode must survive the upload,
			// verified on the target rather than through a download.
			if !probeTargetMode(t, env, remote+"/nested", 0o700) {
				t.Errorf("nested directory mode was not preserved on the target")
			}

			local := downloadBack(t, env, remote)

			if got := readHostFile(t, filepath.Join(local, "top.txt")); got != "top" {
				t.Errorf("top.txt = %q", got)
			}

			if got := readHostFile(t, filepath.Join(local, "nested", "deep.txt")); got != "deep" {
				t.Errorf("nested/deep.txt = %q; nested content must survive the round trip", got)
			}
		},
	}
}

func transferSymlinksPreserve() TestCase {
	return TestCase{
		Category:    CategoryTransfer,
		Name:        "symlinks-preserve",
		Description: "Symbolic links, dangling ones included, survive transfers as links by default",
		Gate: func(caps invoke.Capabilities) (bool, string) {
			return caps.SymlinkPreserve, "target does not declare symlink preservation"
		},
		Run: func(t T, env invoke.Environment) {
			srcDir := t.TempDir()
			writeHostFixture(t, srcDir, "real.txt", "real")

			if err := os.Symlink("real.txt", filepath.Join(srcDir, "link.txt")); err != nil {
				failf(t, "symlink: %v", err)
			}

			if err := os.Symlink("gone", filepath.Join(srcDir, "dangling.txt")); err != nil {
				failf(t, "symlink: %v", err)
			}

			remote := "/tmp/invoke-xfer-" + token(t)
			defer cleanupTargetPath(t, env, remote)

			if err := env.Upload(t.Context(), srcDir, remote); err != nil {
				failf(t, "Upload = %v", err)
			}

			local := downloadBack(t, env, remote)

			if target, err := os.Readlink(filepath.Join(local, "link.txt")); err != nil || target != "real.txt" {
				t.Errorf("link.txt = (%q, %v), want a preserved link to real.txt", target, err)
			}

			if target, err := os.Readlink(filepath.Join(local, "dangling.txt")); err != nil || target != "gone" {
				t.Errorf("dangling.txt = (%q, %v), want the dangling link preserved as-is", target, err)
			}
		},
	}
}

func transferFollowRejectsEscapes() TestCase {
	return TestCase{
		Category:    CategoryTransfer,
		Name:        "follow-rejects-escapes",
		Description: "SymlinkFollow never copies content from outside the transfer root; escaping links fail by name",
		Run: func(t T, env invoke.Environment) {
			outside := writeHostFixture(t, t.TempDir(), "secret.txt", "outside data")

			srcDir := t.TempDir()
			if err := os.Symlink(outside, filepath.Join(srcDir, "escape.txt")); err != nil {
				failf(t, "symlink: %v", err)
			}

			remote := "/tmp/invoke-xfer-" + token(t)
			defer cleanupTargetPath(t, env, remote)

			err := env.Upload(t.Context(), srcDir, remote, invoke.WithSymlinks(invoke.SymlinkFollow))
			if err == nil {
				failf(t, "following a link out of the transfer root succeeded; it must be rejected")
			}

			if !strings.Contains(err.Error(), "escape.txt") {
				t.Errorf("error %q does not name the offending link", err)
			}
		},
	}
}

func transferSpecialFiles() TestCase {
	return TestCase{
		Category:    CategoryTransfer,
		Name:        "special-files-error-by-default",
		Description: "FIFOs and friends fail transfers by name unless WithSkipSpecial omits them",
		Run: func(t T, env invoke.Environment) {
			srcDir := t.TempDir()
			writeHostFixture(t, srcDir, "normal.txt", "normal")

			if !makeFIFO(t, filepath.Join(srcDir, "pipe.fifo")) {
				t.Skipf("FIFO creation unavailable on this host")
			}

			remote := "/tmp/invoke-xfer-" + token(t)
			defer cleanupTargetPath(t, env, remote)

			err := env.Upload(t.Context(), srcDir, remote)
			if err == nil {
				failf(t, "uploading a tree with a FIFO succeeded; special files must error by default")
			}

			if !strings.Contains(err.Error(), "pipe.fifo") {
				t.Errorf("error %q does not name the special file", err)
			}

			if err := env.Upload(t.Context(), srcDir, remote, invoke.WithSkipSpecial()); err != nil {
				failf(t, "Upload with WithSkipSpecial = %v", err)
			}

			if !targetProbe(t, env, "test ! -e "+shellQuote(remote+"/pipe.fifo")) {
				t.Errorf("FIFO was transferred despite WithSkipSpecial")
			}
		},
	}
}

func transferProgressTotals() TestCase {
	return TestCase{
		Category:    CategoryTransfer,
		Name:        "progress-reports-totals",
		Description: "Progress callbacks carry per-file relative paths with real totals reached by Current",
		Run: func(t T, env invoke.Environment) {
			srcDir := t.TempDir()
			writeHostFixture(t, srcDir, "a.txt", strings.Repeat("a", 100))
			writeHostFixture(t, srcDir, "b.txt", strings.Repeat("b", 500))

			remote := "/tmp/invoke-xfer-" + token(t)
			defer cleanupTargetPath(t, env, remote)

			finals := make(map[string]invoke.TransferProgress)

			err := env.Upload(t.Context(), srcDir, remote,
				invoke.WithProgress(func(p invoke.TransferProgress) {
					finals[p.Path] = p
				}))
			if err != nil {
				failf(t, "Upload = %v", err)
			}

			wantSizes := map[string]int64{"a.txt": 100, "b.txt": 500}

			for path, want := range wantSizes {
				final, ok := finals[path]
				if !ok {
					t.Errorf("no progress reported for %q", path)

					continue
				}

				if final.Total != want || final.Current != want {
					t.Errorf("%q final progress = %d/%d, want %d/%d",
						path, final.Current, final.Total, want, want)
				}
			}
		},
	}
}
