package invoketest

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

			assert.ErrorIs(t, env.Upload(ctx, srcDir, remote), context.Canceled,
				"an already-canceled transfer must fail with the context's own error")

			assert.Falsef(t, targetProbe(t, env, "test -e "+shellQuote(remote)),
				"a canceled transfer created %q; it must not touch the destination", remote)
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
			require.NoError(t, os.WriteFile(src, payload, 0o600), "writing binary fixture")

			remote := "/tmp/invoke-xfer-" + token(t)
			defer cleanupTargetPath(t, env, remote)

			require.NoError(t, env.Upload(t.Context(), src, remote), "Upload")

			back := filepath.Join(t.TempDir(), "back.bin")
			require.NoError(t, env.Download(t.Context(), remote, back), "Download")

			got, err := os.ReadFile(back)
			require.NoError(t, err, "reading round-tripped blob")

			assert.Equal(t, payload, got, "binary content changed in transit")
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

			require.NoError(t, env.Upload(t.Context(), big, remote), "seeding Upload")

			dstDir := t.TempDir()
			dst := writeHostFixture(t, dstDir, "dst.bin", "precious local destination")

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			err := env.Download(ctx, remote, dst, invoke.WithProgress(func(_ invoke.TransferProgress) {
				cancel()
			}))
			require.Error(t, err, "canceled Download reported success")

			assert.ErrorIs(t, err, context.Canceled)

			assert.Equal(t, "precious local destination", readHostFile(t, dst),
				"the local destination changed after a canceled download; atomicity was violated")
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
			require.NoError(t, os.Mkdir(emptyDir, 0o750), "mkdir")

			remote := "/tmp/invoke-xfer-" + token(t)
			defer cleanupTargetPath(t, env, remote)

			require.NoError(t, env.Upload(t.Context(), srcDir, remote), "Upload")

			assert.True(t, targetProbe(t, env, "test -d "+shellQuote(remote+"/hollow")),
				"empty directory did not survive the transfer")

			local := downloadBack(t, env, remote)
			assert.Empty(t, readHostFile(t, filepath.Join(local, "empty.txt")),
				"a zero-byte file must round-trip as empty")
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

			require.NoError(t, os.Symlink("real.txt", filepath.Join(srcDir, "link.txt")), "symlink")

			remote := "/tmp/invoke-xfer-" + token(t)
			defer cleanupTargetPath(t, env, remote)

			require.NoError(t,
				env.Upload(t.Context(), srcDir, remote, invoke.WithSymlinks(invoke.SymlinkFollow)), "Upload")

			// On the target, the link path must be a regular file whose
			// content matches the target — not a symlink, not missing.
			linkPath := shellQuote(remote + "/link.txt")
			assert.True(t, targetProbe(t, env, "test -f "+linkPath+" && test ! -L "+linkPath),
				"followed link is not a regular file on the target")

			local := downloadBack(t, env, remote)
			assert.Equal(t, "followed content", readHostFile(t, filepath.Join(local, "link.txt")),
				"a followed link must carry the target's content")
		},
	}
}

// writeHostFixture creates a host-side file with content at fixtureMode,
// applied explicitly so umask cannot interfere.
func writeHostFixture(t T, dir, name, content string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	require.NoErrorf(t, os.WriteFile(path, []byte(content), fixtureMode), "writing fixture %q", path)
	require.NoErrorf(t, os.Chmod(path, fixtureMode), "chmod fixture %q", path)

	return path
}

// downloadBack fetches a target path into a fresh host location and
// returns that location.
func downloadBack(t T, env invoke.Environment, remote string) string {
	t.Helper()

	local := filepath.Join(t.TempDir(), "downloaded")
	require.NoErrorf(t, env.Download(t.Context(), remote, local), "Download(%q)", remote)

	return local
}

// readHostFile reads a host-side file or fails the contract.
func readHostFile(t T, path string) string {
	t.Helper()

	content, err := os.ReadFile(path)
	require.NoErrorf(t, err, "reading %q", path)

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

			require.NoError(t, env.Upload(t.Context(), src, remote), "Upload")

			assert.Truef(t, probeTargetMode(t, env, remote, fixtureMode),
				"target mode is not %v; upload must preserve the source mode umask-proof", fixtureMode)

			local := downloadBack(t, env, remote)
			assert.Equal(t, "payload", readHostFile(t, local))
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

			require.NoError(t, env.Upload(t.Context(), first, remote), "first Upload")
			require.NoError(t,
				env.Upload(t.Context(), second, remote, invoke.WithMode(overrideMode)), "second Upload")

			assert.Truef(t, probeTargetMode(t, env, remote, overrideMode),
				"target mode is not %v; WithMode must apply on overwrite", overrideMode)

			assert.Equal(t, "new content", readHostFile(t, downloadBack(t, env, remote)),
				"the overwriting upload's content must win")
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

			require.NoError(t, env.Upload(t.Context(), good, remote), "seeding Upload")
			require.NoError(t, os.Chmod(unreadable, 0), "chmod")

			require.Error(t, env.Upload(t.Context(), unreadable, remote),
				"uploading an unreadable source succeeded, want an error")

			assert.Equal(t, "precious destination", readHostFile(t, downloadBack(t, env, remote)),
				"the destination changed after a failed transfer; atomicity was violated")
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

			require.NoError(t, env.Upload(t.Context(), good, remote), "seeding Upload")

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			err := env.Upload(ctx, big, remote, invoke.WithProgress(func(_ invoke.TransferProgress) {
				cancel()
			}))
			require.Error(t, err, "canceled Upload reported success")

			assert.ErrorIs(t, err, context.Canceled)

			assert.Equal(t, "precious destination", readHostFile(t, downloadBack(t, env, remote)),
				"the destination changed after a canceled transfer; atomicity was violated")
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
			require.NoError(t, os.Mkdir(nested, 0o700), "mkdir")

			// os.Mkdir is umask-subject; pin the intended dir mode.
			require.NoError(t, os.Chmod(nested, 0o700), "chmod")

			writeHostFixture(t, nested, "deep.txt", "deep")

			base := "/tmp/invoke-xfer-" + token(t)
			remote := base + "/made/parents/tree"

			defer cleanupTargetPath(t, env, base)

			require.NoError(t, env.Upload(t.Context(), srcDir, remote), "Upload")

			// The nested directory's own mode must survive the upload,
			// verified on the target rather than through a download.
			assert.True(t, probeTargetMode(t, env, remote+"/nested", 0o700),
				"nested directory mode was not preserved on the target")

			local := downloadBack(t, env, remote)

			assert.Equal(t, "top", readHostFile(t, filepath.Join(local, "top.txt")))
			assert.Equal(t, "deep", readHostFile(t, filepath.Join(local, "nested", "deep.txt")),
				"nested content must survive the round trip")
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

			require.NoError(t, os.Symlink("real.txt", filepath.Join(srcDir, "link.txt")), "symlink")
			require.NoError(t, os.Symlink("gone", filepath.Join(srcDir, "dangling.txt")), "symlink")

			remote := "/tmp/invoke-xfer-" + token(t)
			defer cleanupTargetPath(t, env, remote)

			require.NoError(t, env.Upload(t.Context(), srcDir, remote), "Upload")

			local := downloadBack(t, env, remote)

			linkTarget, linkErr := os.Readlink(filepath.Join(local, "link.txt"))
			assert.NoError(t, linkErr, "link.txt must survive as a link")
			assert.Equal(t, "real.txt", linkTarget, "link.txt must be preserved as a link to real.txt")

			danglingTarget, danglingErr := os.Readlink(filepath.Join(local, "dangling.txt"))
			assert.NoError(t, danglingErr, "dangling.txt must survive as a link")
			assert.Equal(t, "gone", danglingTarget, "the dangling link must be preserved as-is")
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
			require.NoError(t, os.Symlink(outside, filepath.Join(srcDir, "escape.txt")), "symlink")

			remote := "/tmp/invoke-xfer-" + token(t)
			defer cleanupTargetPath(t, env, remote)

			err := env.Upload(t.Context(), srcDir, remote, invoke.WithSymlinks(invoke.SymlinkFollow))
			require.Error(t, err,
				"following a link out of the transfer root succeeded; it must be rejected")

			assert.ErrorContains(t, err, "escape.txt", "the error must name the offending link")
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
			require.Error(t, err,
				"uploading a tree with a FIFO succeeded; special files must error by default")

			assert.ErrorContains(t, err, "pipe.fifo", "the error must name the special file")

			require.NoError(t,
				env.Upload(t.Context(), srcDir, remote, invoke.WithSkipSpecial()), "Upload with WithSkipSpecial")

			assert.True(t, targetProbe(t, env, "test ! -e "+shellQuote(remote+"/pipe.fifo")),
				"FIFO was transferred despite WithSkipSpecial")
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
			require.NoError(t, err, "Upload")

			wantSizes := map[string]int64{"a.txt": 100, "b.txt": 500}

			for path, want := range wantSizes {
				final, ok := finals[path]
				if !ok {
					assert.Failf(t, "missing progress", "no progress reported for %q", path)

					continue
				}

				assert.Equalf(t, want, final.Total, "%q: Total must be the file's real size", path)
				assert.Equalf(t, want, final.Current, "%q: Current must reach the total", path)
			}
		},
	}
}
