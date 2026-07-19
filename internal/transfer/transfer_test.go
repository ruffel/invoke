package transfer_test

import (
	"context"
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
