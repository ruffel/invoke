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
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}

			dirs = append(dirs, pendingDir{path: target, mode: headerMode(header, cfg)})

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
		return extractFile(ctx, tr, target, header, cfg)

	case tar.TypeSymlink:
		return extractSymlink(header.Linkname, target)

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

// headerMode is the mode an extracted entry should carry.
func headerMode(header *tar.Header, cfg invoke.TransferConfig) fs.FileMode {
	if cfg.Mode != nil {
		return *cfg.Mode
	}

	return header.FileInfo().Mode().Perm()
}

// extractFile writes one regular file, reporting progress as bytes land.
func extractFile(ctx context.Context, tr io.Reader, target string, header *tar.Header, cfg invoke.TransferConfig) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
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

// extractSymlink recreates a link, replacing whatever occupies the path.
func extractSymlink(linkTarget, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
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

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}

	_ = os.Remove(target)

	return os.Link(source, target)
}
