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

	if err := os.WriteFile(secret, []byte("outside data"), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	srcDir := filepath.Join(base, "src")
	if err := os.Mkdir(srcDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	dstDir := filepath.Join(base, "dst")
	name := ".." + string(filepath.Separator) + "secret.txt"

	err := transfer.Copy(t.Context(), hostileFS{root: srcDir, name: name}, srcDir,
		transfer.HostFS{}, dstDir, invoke.TransferConfig{})
	if err == nil {
		t.Fatal("Copy accepted an entry name traversing out of the transfer root")
	}

	// The decisive assertion: the outside file must not have been read
	// and rewritten anywhere.
	if _, statErr := os.Stat(filepath.Join(base, "secret.txt.copy")); statErr == nil {
		t.Error("the outside file was copied")
	}

	entries, readErr := os.ReadDir(base)
	if readErr != nil {
		t.Fatalf("reading base: %v", readErr)
	}

	for _, entry := range entries {
		if entry.Name() != "secret.txt" && entry.Name() != "src" && entry.Name() != "dst" {
			t.Errorf("transfer wrote %q outside the destination root", entry.Name())
		}
	}

	if got, readErr := os.ReadFile(filepath.Join(dstDir, "secret.txt")); readErr == nil {
		t.Errorf("outside content landed in the destination: %q", got)
	}
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
			if err == nil {
				t.Fatalf("Copy accepted the entry name %q; it must be refused", name)
			}

			if !strings.Contains(err.Error(), "usable name") && !strings.Contains(err.Error(), "escapes") {
				t.Errorf("error %q does not report the entry name as the problem", err)
			}
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

	if err := os.WriteFile(filepath.Join(srcDir, name), []byte("payload"), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	dstDir := filepath.Join(t.TempDir(), "dst")

	if err := transfer.Copy(t.Context(), posixFS{}, srcDir, transfer.HostFS{}, dstDir, invoke.TransferConfig{}); err != nil {
		t.Fatalf("Copy = %v, want a legitimate backslash filename to transfer", err)
	}

	got, err := os.ReadFile(filepath.Join(dstDir, name))
	if err != nil {
		t.Fatalf("reading transferred file: %v", err)
	}

	if string(got) != "payload" {
		t.Errorf("content = %q, want %q", got, "payload")
	}
}

// TestCopyRejectsCanceledContext checks the engine refuses before it
// creates anything, including for a source with no entries to check.
func TestCopyRejectsCanceledContext(t *testing.T) {
	t.Parallel()

	srcDir := t.TempDir()
	dstDir := filepath.Join(t.TempDir(), "dst")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := transfer.Copy(ctx, transfer.HostFS{}, srcDir, transfer.HostFS{}, dstDir, invoke.TransferConfig{}); err == nil {
		t.Fatal("Copy with a canceled context reported success")
	}

	if _, err := os.Stat(dstDir); err == nil {
		t.Error("a canceled Copy created the destination")
	}
}
