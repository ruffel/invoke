package docker

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ruffel/invoke"
)

// extractTree writes an archive into dst, applying the transfer's policy.
//
// Every entry is placed by the destination's own path rules and checked
// to land inside dst: an archive names its own entries, and the daemon is
// not the only thing that can produce one.
func extractTree(ctx context.Context, tr *tar.Reader, dst string, cfg invoke.TransferConfig) error {
	// Directory modes are applied after the walk, deepest first, so a
	// read-only directory cannot block writing its own contents.
	type pendingDir struct {
		path string
		mode fs.FileMode
	}

	var dirs []pendingDir

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return err
		}

		target, err := entryPath(dst, header.Name)
		if err != nil {
			return err
		}

		if header.Typeflag == tar.TypeDir {
			if err := ensureContainedDir(dst, target); err != nil {
				return err
			}

			// A directory keeps the mode its header carries; the mode
			// override is a statement about files.
			dirs = append(dirs, pendingDir{path: target, mode: header.FileInfo().Mode().Perm()})

			continue
		}

		if err := extractEntry(ctx, tr, dst, target, header, cfg); err != nil {
			return err
		}
	}

	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i].path, dirs[i].mode); err != nil {
			return err
		}
	}

	return nil
}

// extractEntry writes one non-directory archive entry.
func extractEntry(
	ctx context.Context,
	tr *tar.Reader,
	dst, target string,
	header *tar.Header,
	cfg invoke.TransferConfig,
) error {
	switch header.Typeflag {
	case tar.TypeReg:
		return extractFile(ctx, tr, dst, target, header, cfg)

	case tar.TypeSymlink:
		return extractSymlink(dst, header.Linkname, target)

	case tar.TypeLink:
		return extractHardlink(dst, header.Linkname, target)

	default:
		// Devices, sockets and pipes cannot be reproduced faithfully
		// here. Dropping them is what makes a transfer look complete
		// when it is not, so they are refused by name.
		if cfg.SkipSpecial {
			return nil
		}

		return fmt.Errorf("unsupported special file %q in archive; use WithSkipSpecial to omit it", header.Name)
	}
}

// entryPath resolves an archive entry's name against dst and refuses one
// that would land outside it.
func entryPath(dst, name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", fmt.Errorf("archive entry %q escapes the destination", name)
	}

	target := filepath.Join(dst, clean)
	if !containsPath(dst, target) && target != dst {
		return "", fmt.Errorf("archive entry %q escapes the destination", name)
	}

	return target, nil
}

// containedComponents lists the paths from the destination root down to p,
// one per component, so each can be examined before it is walked through.
func containedComponents(dst, p string) ([]string, error) {
	rel, err := filepath.Rel(dst, p)
	if err != nil {
		return nil, err
	}

	if rel == "." {
		return nil, nil
	}

	parts := strings.Split(rel, string(filepath.Separator))
	paths := make([]string, 0, len(parts))
	current := dst

	for _, name := range parts {
		current = filepath.Join(current, name)
		paths = append(paths, current)
	}

	return paths, nil
}

// ensureContainedDir creates the directories from the destination root
// down to dir, refusing any component already present as a symbolic link.
//
// An archive names its own entries and need not order them, so an entry's
// parent is usually created here rather than by a directory entry of its
// own. Creating it a component at a time is what makes the check possible:
// MkdirAll resolves the whole path it is given, and a link among those
// components would be followed before anything could object to it.
func ensureContainedDir(dst, dir string) error {
	components, err := containedComponents(dst, dir)
	if err != nil {
		return err
	}

	for _, component := range components {
		if err := makeContainedDir(component); err != nil {
			return err
		}
	}

	return nil
}

// makeContainedDir creates one directory inside the destination, merging
// with a directory already there and refusing a symbolic link.
//
// The archive chose this path. A link standing where it expects a
// directory would carry the rest of the extraction wherever the link
// points, while every name the extractor forms still read as contained.
func makeContainedDir(dir string) error {
	mkdirErr := os.Mkdir(dir, 0o755)
	if mkdirErr == nil {
		return nil
	}

	info, err := os.Lstat(dir)
	if err != nil {
		return mkdirErr
	}

	if info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("archive entry %q is a symbolic link; refusing to extract through it", dir)
	}

	if info.IsDir() {
		return nil
	}

	return mkdirErr
}

// verifyContained reports whether any component between the destination
// root and p is a symbolic link, for a path the archive names but the
// extractor does not create.
func verifyContained(dst, p string) error {
	components, err := containedComponents(dst, p)
	if err != nil {
		return err
	}

	for _, component := range components {
		info, err := os.Lstat(component)
		if err != nil {
			return err
		}

		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("archive entry %q resolves through the symbolic link %q", p, component)
		}
	}

	return nil
}

// headerMode is the mode an extracted file should carry: the
// transfer's override when it set one, and otherwise the mode the
// archive recorded. Directories do not pass through here — the
// override is a statement about files, and a directory keeps the mode
// its header carries.
func headerMode(header *tar.Header, cfg invoke.TransferConfig) fs.FileMode {
	if cfg.Mode != nil {
		return *cfg.Mode
	}

	return header.FileInfo().Mode().Perm()
}

// extractFile writes one regular file, reporting progress as bytes land.
func extractFile(
	ctx context.Context,
	tr io.Reader,
	dst, target string,
	header *tar.Header,
	cfg invoke.TransferConfig,
) error {
	if err := ensureContainedDir(dst, filepath.Dir(target)); err != nil {
		return err
	}

	// A link already occupying the target would be followed by the create
	// below, writing through it. Extraction replaces what it finds, so the
	// link goes rather than the file it points at.
	if err := removeSymlink(target); err != nil {
		return err
	}

	mode := headerMode(header, cfg)

	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}

	defer func() { _ = file.Close() }()

	var reader io.Reader = &ctxReader{ctx: ctx, inner: tr}
	if cfg.Progress != nil {
		reader = &progressReader{
			inner: reader,
			path:  strings.TrimPrefix(filepath.ToSlash(header.Name), archiveRootPrefix(header.Name)),
			total: header.Size,
			fn:    cfg.Progress,
		}
	}

	if _, err := io.Copy(file, reader); err != nil {
		return err
	}

	// Applied after creation, so the process umask cannot narrow it.
	if err := file.Chmod(mode); err != nil {
		return err
	}

	return file.Close()
}

// archiveRootPrefix returns the leading path element an archive's entries
// share, so progress reports a path relative to the transfer root rather
// than to the archive.
func archiveRootPrefix(name string) string {
	slashed := filepath.ToSlash(name)

	if idx := strings.Index(slashed, "/"); idx >= 0 {
		return slashed[:idx+1]
	}

	return ""
}

// removeSymlink clears a symbolic link occupying an extraction target, so
// creating the entry there cannot follow it out of the tree.
func removeSymlink(target string) error {
	info, err := os.Lstat(target)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return err
	}

	if info.Mode()&fs.ModeSymlink == 0 {
		return nil
	}

	return os.Remove(target)
}

// extractSymlink recreates a link, replacing whatever occupies the path.
func extractSymlink(dst, linkTarget, target string) error {
	if err := ensureContainedDir(dst, filepath.Dir(target)); err != nil {
		return err
	}

	// A link is created at a temporary name and moved into place, so an
	// existing entry is replaced in one step rather than removed first.
	tmp := filepath.Join(filepath.Dir(target), ".invoke-link-"+filepath.Base(target)+".tmp")

	_ = os.Remove(tmp)

	if err := os.Symlink(linkTarget, tmp); err != nil {
		return err
	}

	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)

		return err
	}

	return nil
}

// extractHardlink recreates a hard link to an entry already extracted.
func extractHardlink(dst, linkName, target string) error {
	source, err := entryPath(dst, linkName)
	if err != nil {
		return err
	}

	// The source is a path the archive names rather than one the extractor
	// creates, so a link among its components would reach a file outside
	// the tree and hard-link it back in.
	if err := verifyContained(dst, source); err != nil {
		return err
	}

	if err := ensureContainedDir(dst, filepath.Dir(target)); err != nil {
		return err
	}

	_ = os.Remove(target)

	return os.Link(source, target)
}
