package docker

import (
	"archive/tar"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tarEntry describes one member of an archive built for these tests. An
// archive arrives from the daemon, but the daemon is not the only thing
// that can produce one, so these are written by hand.
type tarEntry struct {
	name     string
	typeflag byte
	linkname string
	body     string
	mode     int64
}

// buildArchive assembles the entries into an archive, in order.
func buildArchive(t *testing.T, entries []tarEntry) *tar.Reader {
	t.Helper()

	var buf bytes.Buffer

	writer := tar.NewWriter(&buf)

	for _, entry := range entries {
		mode := entry.mode
		if mode == 0 {
			mode = 0o644
		}

		require.NoError(t, writer.WriteHeader(&tar.Header{
			Name:     entry.name,
			Typeflag: entry.typeflag,
			Linkname: entry.linkname,
			Mode:     mode,
			Size:     int64(len(entry.body)),
		}), "writing the header for %q", entry.name)

		if entry.body != "" {
			_, err := writer.Write([]byte(entry.body))
			require.NoError(t, err, "writing the body of %q", entry.name)
		}
	}

	require.NoError(t, writer.Close(), "closing the archive")

	return tar.NewReader(&buf)
}

// TestExtractRefusesToWriteThroughASymlinkedParent pins the containment
// boundary against the archive itself. Each entry's name is checked
// against the destination, and both of these names pass: what leaves the
// destination is not the name but the link the first entry plants, which
// the second one is then written through.
func TestExtractRefusesToWriteThroughASymlinkedParent(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	dst := filepath.Join(base, "dst")
	outside := filepath.Join(base, "outside")

	require.NoError(t, os.MkdirAll(dst, 0o755), "destination")
	require.NoError(t, os.MkdirAll(outside, 0o755), "outside directory")

	archive := buildArchive(t, []tarEntry{
		{name: "root/", typeflag: tar.TypeDir, mode: 0o755},
		{name: "root/sub", typeflag: tar.TypeSymlink, linkname: outside},
		{name: "root/sub/payload.txt", typeflag: tar.TypeReg, body: "payload"},
	})

	err := extractTree(t.Context(), archive, dst, invoke.TransferConfig{})
	require.Error(t, err, "an extraction through a planted link reported success")

	assert.ErrorContains(t, err, "symbolic link", "the error does not say why the extraction stopped")

	assert.NoFileExists(t, filepath.Join(outside, "payload.txt"),
		"the extraction wrote through the planted link, outside the destination")
}

// TestExtractReplacesALinkAtTheTargetRatherThanWritingThroughIt covers the
// same escape one component further down, where the link occupies the
// entry's own path rather than a parent of it.
func TestExtractReplacesALinkAtTheTargetRatherThanWritingThroughIt(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	dst := filepath.Join(base, "dst")
	outside := filepath.Join(base, "outside")

	require.NoError(t, os.MkdirAll(dst, 0o755), "destination")
	require.NoError(t, os.MkdirAll(outside, 0o755), "outside directory")

	victim := filepath.Join(outside, "victim.txt")
	require.NoError(t, os.WriteFile(victim, []byte("original"), 0o600), "writing fixture")

	archive := buildArchive(t, []tarEntry{
		{name: "root/", typeflag: tar.TypeDir, mode: 0o755},
		{name: "root/f.txt", typeflag: tar.TypeSymlink, linkname: victim},
		{name: "root/f.txt", typeflag: tar.TypeReg, body: "overwritten"},
	})

	require.NoError(t, extractTree(t.Context(), archive, dst, invoke.TransferConfig{}),
		"replacing a link with a file is an ordinary extraction")

	got, err := os.ReadFile(victim)
	require.NoError(t, err, "reading the file the link pointed at")

	assert.Equal(t, "original", string(got),
		"the extraction wrote through the link at its own target")
}

// TestExtractRefusesAHardlinkResolvingThroughASymlink covers the path an
// archive names but the extractor does not create: a hard link's source
// resolves at link time, so a planted link among its components would
// reach a file outside the tree and carry it back in.
func TestExtractRefusesAHardlinkResolvingThroughASymlink(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	dst := filepath.Join(base, "dst")
	outside := filepath.Join(base, "outside")

	require.NoError(t, os.MkdirAll(dst, 0o755), "destination")
	require.NoError(t, os.MkdirAll(outside, 0o755), "outside directory")
	require.NoError(t,
		os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600), "writing fixture")

	archive := buildArchive(t, []tarEntry{
		{name: "root/", typeflag: tar.TypeDir, mode: 0o755},
		{name: "root/sub", typeflag: tar.TypeSymlink, linkname: outside},
		{name: "root/stolen.txt", typeflag: tar.TypeLink, linkname: "root/sub/secret.txt"},
	})

	err := extractTree(t.Context(), archive, dst, invoke.TransferConfig{})
	require.Error(t, err, "a hard link resolving through a planted link reported success")

	assert.NoFileExists(t, filepath.Join(dst, "root", "stolen.txt"),
		"a file outside the tree was hard-linked into it")
}

// TestExtractCarriesAnOrdinaryTree guards the other side of the rule: the
// containment checks must not refuse the trees that motivated preserving
// links in the first place.
func TestExtractCarriesAnOrdinaryTree(t *testing.T) {
	t.Parallel()

	dst := t.TempDir()

	archive := buildArchive(t, []tarEntry{
		{name: "root/", typeflag: tar.TypeDir, mode: 0o755},
		{name: "root/lib/", typeflag: tar.TypeDir, mode: 0o755},
		{name: "root/lib/real.txt", typeflag: tar.TypeReg, body: "payload"},
		{name: "root/link.txt", typeflag: tar.TypeSymlink, linkname: "lib/real.txt"},
	})

	require.NoError(t, extractTree(t.Context(), archive, dst, invoke.TransferConfig{}),
		"an ordinary tree must still extract")

	got, err := os.ReadFile(filepath.Join(dst, "root", "lib", "real.txt"))
	require.NoError(t, err, "reading the extracted file")

	assert.Equal(t, "payload", string(got))

	info, err := os.Lstat(filepath.Join(dst, "root", "link.txt"))
	require.NoError(t, err, "the link was not extracted")

	assert.NotZero(t, info.Mode()&fs.ModeSymlink, "the link was not preserved as a link")
}
