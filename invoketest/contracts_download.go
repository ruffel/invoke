package invoketest

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The transfer options are promises about both directions, but most of
// the contracts above prove them through Upload, reading the result back
// with a plain Download. These are the same promises made of Download:
// the target holds the tree, seeded by an Upload, and the policy under
// test applies on the way back.
func downloadContracts() []TestCase {
	return []TestCase{
		downloadSymlinkFollowCopiesContent(),
		downloadFollowRejectsEscapes(),
		downloadProgressTotals(),
		downloadFailurePreservesDestination(),
		downloadTreeMergesIntoExisting(),
	}
}

func downloadSymlinkFollowCopiesContent() TestCase {
	return TestCase{
		Category:    CategoryTransfer,
		Name:        "download-symlink-follow-copies-content",
		Description: "SymlinkFollow on Download replaces an in-root link with its target's content",
		Gate: func(caps invoke.Capabilities) (bool, string) {
			return caps.SymlinkPreserve, "seeding a link on the target needs symlink preservation"
		},
		Run: func(t T, env invoke.Environment) {
			srcDir := t.TempDir()
			writeHostFixture(t, srcDir, "real.txt", "followed content")
			require.NoError(t, os.Symlink("real.txt", filepath.Join(srcDir, "link.txt")), "symlink")

			remote := "/tmp/invoke-xfer-" + rand.Text()
			defer cleanupTargetPath(t, env, remote)

			require.NoError(t, env.Upload(t.Context(), srcDir, remote), "seeding Upload")

			local := filepath.Join(t.TempDir(), "downloaded")
			require.NoError(t,
				env.Download(t.Context(), remote, local, invoke.WithSymlinks(invoke.SymlinkFollow)), "Download")

			info, err := os.Lstat(filepath.Join(local, "link.txt"))
			require.NoError(t, err, "the followed link must arrive")
			assert.True(t, info.Mode().IsRegular(), "a followed link must arrive as a regular file, not a link")
			assert.Equal(t, "followed content", readHostFile(t, filepath.Join(local, "link.txt")),
				"a followed link must carry its target's content")
		},
	}
}

func downloadFollowRejectsEscapes() TestCase {
	return TestCase{
		Category:    CategoryTransfer,
		Name:        "download-follow-rejects-escapes",
		Description: "SymlinkFollow on Download never copies content from outside the transfer root; escaping links fail by name",
		Gate: func(caps invoke.Capabilities) (bool, string) {
			return caps.SymlinkPreserve, "seeding a link on the target needs symlink preservation"
		},
		Run: func(t T, env invoke.Environment) {
			tok := rand.Text()

			// The secret sits on the target, beside the tree rather than
			// in it, and the tree's link points at it by absolute path.
			outside := "/tmp/invoke-outside-" + tok
			defer cleanupTargetPath(t, env, outside)

			secret := writeHostFixture(t, t.TempDir(), "secret.txt", "outside data")
			require.NoError(t, env.Upload(t.Context(), secret, outside), "seeding the outside file")

			srcDir := t.TempDir()
			require.NoError(t, os.Symlink(outside, filepath.Join(srcDir, "escape.txt")), "symlink")

			remote := "/tmp/invoke-xfer-" + tok
			defer cleanupTargetPath(t, env, remote)

			require.NoError(t, env.Upload(t.Context(), srcDir, remote), "seeding Upload")

			local := filepath.Join(t.TempDir(), "downloaded")
			err := env.Download(t.Context(), remote, local, invoke.WithSymlinks(invoke.SymlinkFollow))
			require.Error(t, err, "following a link out of the transfer root succeeded; it must be rejected")
			assert.ErrorContains(t, err, "escape.txt", "the error must name the offending link")

			// The decisive check: whatever else arrived, the secret did not.
			escaped := filepath.Join(local, "escape.txt")
			if content, readErr := os.ReadFile(escaped); readErr == nil { //nolint:gosec // A path under the contract's own temp dir.
				assert.NotContains(t, string(content), "outside data", "content from outside the root was copied")
			}
		},
	}
}

func downloadProgressTotals() TestCase {
	return TestCase{
		Category:    CategoryTransfer,
		Name:        "download-progress-reports-totals",
		Description: "Download progress carries per-file relative paths with real totals reached by Current",
		Run: func(t T, env invoke.Environment) {
			srcDir := t.TempDir()
			writeHostFixture(t, srcDir, "a.txt", strings.Repeat("a", 100))
			writeHostFixture(t, srcDir, "b.txt", strings.Repeat("b", 500))

			// Nested, so a path reported as the bare file name rather than
			// relative to the root shows.
			require.NoError(t, os.Mkdir(filepath.Join(srcDir, "nested"), 0o750), "mkdir")
			writeHostFixture(t, filepath.Join(srcDir, "nested"), "c.txt", strings.Repeat("c", 100))

			remote := "/tmp/invoke-xfer-" + rand.Text()
			defer cleanupTargetPath(t, env, remote)

			require.NoError(t, env.Upload(t.Context(), srcDir, remote), "seeding Upload")

			// Guarded: progress callbacks may arrive concurrently when a
			// provider copies files in parallel.
			var mu sync.Mutex

			finals := make(map[string]invoke.TransferProgress)

			local := filepath.Join(t.TempDir(), "downloaded")
			err := env.Download(t.Context(), remote, local,
				invoke.WithProgress(func(p invoke.TransferProgress) {
					mu.Lock()
					defer mu.Unlock()

					finals[p.Path] = p
				}))
			require.NoError(t, err, "Download")

			for path, want := range map[string]int64{"a.txt": 100, "b.txt": 500, "nested/c.txt": 100} {
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

func downloadFailurePreservesDestination() TestCase {
	return TestCase{
		Category:    CategoryTransfer,
		Name:        "download-failure-preserves-destination",
		Description: "A failed Download leaves an existing local destination byte-for-byte intact",
		Run: func(t T, env invoke.Environment) {
			local := writeHostFixture(t, t.TempDir(), "precious.txt", "precious destination")

			missing := "/tmp/invoke-missing-" + rand.Text()

			require.Error(t, env.Download(t.Context(), missing, local),
				"downloading a path that does not exist succeeded, want an error")

			assert.Equal(t, "precious destination", readHostFile(t, local),
				"the destination changed after a failed download; atomicity was violated")
		},
	}
}

func downloadTreeMergesIntoExisting() TestCase {
	return TestCase{
		Category:    CategoryTransfer,
		Name:        "download-merges-into-existing",
		Description: "Downloading a tree onto an existing local directory merges: namesakes are replaced, everything else survives",
		Run: func(t T, env invoke.Environment) {
			srcDir := t.TempDir()
			writeHostFixture(t, srcDir, "shared.txt", "new")
			writeHostFixture(t, srcDir, "added.txt", "added")

			remote := "/tmp/invoke-xfer-" + rand.Text()
			defer cleanupTargetPath(t, env, remote)

			require.NoError(t, env.Upload(t.Context(), srcDir, remote), "seeding Upload")

			local := t.TempDir()
			writeHostFixture(t, local, "keep.txt", "kept")
			writeHostFixture(t, local, "shared.txt", "old")

			require.NoError(t, env.Download(t.Context(), remote, local), "Download onto the existing directory")

			assert.Equal(t, "kept", readHostFile(t, filepath.Join(local, "keep.txt")),
				"a file only the destination held must survive the merge")
			assert.Equal(t, "new", readHostFile(t, filepath.Join(local, "shared.txt")),
				"a file the tree carries must replace its namesake")
			assert.Equal(t, "added", readHostFile(t, filepath.Join(local, "added.txt")))
		},
	}
}
