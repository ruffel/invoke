package invoketest

import (
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

func transferContracts() []TestCase {
	return []TestCase{
		transferRoundTrip(),
		transferModeOverride(),
		transferFailurePreservesDestination(),
		transferCancelPreservesDestination(),
		transferTreeCreatesParents(),
		transferSymlinksPreserve(),
		transferFollowRejectsEscapes(),
		transferSpecialFiles(),
		transferProgressTotals(),
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
			if err := os.Mkdir(nested, 0o755); err != nil {
				failf(t, "mkdir: %v", err)
			}

			writeHostFixture(t, nested, "deep.txt", "deep")

			base := "/tmp/invoke-xfer-" + token(t)
			remote := base + "/made/parents/tree"

			defer cleanupTargetPath(t, env, base)

			if err := env.Upload(t.Context(), srcDir, remote); err != nil {
				failf(t, "Upload = %v", err)
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
