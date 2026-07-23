package docker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeReplaceFixture creates path's parents and writes content there.
func writeReplaceFixture(t *testing.T, path, content string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// TestReplacePathReplacesAnyExistingEntry pins the host-side half of the
// replace step to the same promise the container-side script keeps: a
// download replaces whatever occupies its destination, whatever kind of
// entry that is.
func TestReplacePathReplacesAnyExistingEntry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		src     func(t *testing.T, dir string) string
		dst     func(t *testing.T, dir string) string
		probe   string // path under dst whose content proves the arrival won
		content string
	}{
		{
			name: "file over file",
			src: func(t *testing.T, dir string) string {
				t.Helper()

				p := filepath.Join(dir, "staging", "arrived")
				writeReplaceFixture(t, p, "new")

				return p
			},
			dst: func(t *testing.T, dir string) string {
				t.Helper()

				p := filepath.Join(dir, "dest")
				writeReplaceFixture(t, p, "old")

				return p
			},
			probe:   "",
			content: "new",
		},
		{
			name: "directory over file",
			src: func(t *testing.T, dir string) string {
				t.Helper()

				p := filepath.Join(dir, "staging", "arrived")
				writeReplaceFixture(t, filepath.Join(p, "inner.txt"), "new tree")

				return p
			},
			dst: func(t *testing.T, dir string) string {
				t.Helper()

				p := filepath.Join(dir, "dest")
				writeReplaceFixture(t, p, "old file")

				return p
			},
			probe:   "inner.txt",
			content: "new tree",
		},
		{
			name: "directory over symlink",
			src: func(t *testing.T, dir string) string {
				t.Helper()

				p := filepath.Join(dir, "staging", "arrived")
				writeReplaceFixture(t, filepath.Join(p, "inner.txt"), "new tree")

				return p
			},
			dst: func(t *testing.T, dir string) string {
				t.Helper()

				target := filepath.Join(dir, "elsewhere")
				writeReplaceFixture(t, target, "link target")

				p := filepath.Join(dir, "dest")
				require.NoError(t, os.Symlink(target, p))

				return p
			},
			probe:   "inner.txt",
			content: "new tree",
		},
		{
			name: "file over directory",
			src: func(t *testing.T, dir string) string {
				t.Helper()

				p := filepath.Join(dir, "staging", "arrived")
				writeReplaceFixture(t, p, "new")

				return p
			},
			dst: func(t *testing.T, dir string) string {
				t.Helper()

				p := filepath.Join(dir, "dest")
				writeReplaceFixture(t, filepath.Join(p, "stale.txt"), "old tree")

				return p
			},
			probe:   "",
			content: "new",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			src := tt.src(t, dir)
			dst := tt.dst(t, dir)

			require.NoError(t, replacePath(src, dst),
				"a download replaces whatever occupies its destination")

			read := dst
			if tt.probe != "" {
				read = filepath.Join(dst, tt.probe)
			}

			got, err := os.ReadFile(read)
			require.NoError(t, err)

			assert.Equal(t, tt.content, string(got), "the arrival must win")
		})
	}
}

// TestReplacePathFailurePreservesTheDestination pins the aside-restore
// promise: a replacement that cannot move into place leaves the
// original destination exactly where it was — never deleted ahead of a
// replacement that did not arrive.
func TestReplacePathFailurePreservesTheDestination(t *testing.T) {
	t.Parallel()

	const readOnlyDir = os.FileMode(0o555)

	dir := t.TempDir()

	dst := filepath.Join(dir, "dest")
	writeReplaceFixture(t, filepath.Join(dst, "keep.txt"), "precious destination")

	stagingParent := filepath.Join(dir, "staging")
	src := filepath.Join(stagingParent, "arrived")
	writeReplaceFixture(t, filepath.Join(src, "new.txt"), "never lands")

	// The final rename must remove src from its parent, which a
	// read-only parent refuses — after the destination has already been
	// set aside.
	require.NoError(t, os.Chmod(stagingParent, readOnlyDir))

	t.Cleanup(func() { _ = os.Chmod(stagingParent, 0o755) })

	require.Error(t, replacePath(src, dst), "the move cannot succeed")

	got, err := os.ReadFile(filepath.Join(dst, "keep.txt"))
	require.NoError(t, err,
		"the destination was deleted ahead of a replacement that did not arrive")

	assert.Equal(t, "precious destination", string(got),
		"a failed replacement must leave the original destination intact")
}
