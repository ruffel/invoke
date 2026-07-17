package local

import (
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

// Upload copies a file or directory tree between local paths. On this
// provider Upload and Download are the same operation; both exist so the
// Environment contract holds regardless of target.
func (e *Environment) Upload(ctx context.Context, localPath, remotePath string, opts ...invoke.TransferOption) error {
	if err := e.checkOpen("upload"); err != nil {
		return err
	}

	if err := copyPath(ctx, localPath, remotePath, invoke.NewTransferConfig(opts...)); err != nil {
		return fmt.Errorf("local: upload: %w", err)
	}

	return nil
}

// Download copies a file or directory tree between local paths, with the
// same semantics as Upload.
func (e *Environment) Download(ctx context.Context, remotePath, localPath string, opts ...invoke.TransferOption) error {
	if err := e.checkOpen("download"); err != nil {
		return err
	}

	if err := copyPath(ctx, remotePath, localPath, invoke.NewTransferConfig(opts...)); err != nil {
		return fmt.Errorf("local: download: %w", err)
	}

	return nil
}

// copyPath copies src to dst after validating the transfer is safe: the
// paths must not alias each other, and a directory may not be copied into
// its own subtree.
func copyPath(ctx context.Context, src, dst string, cfg invoke.TransferConfig) error {
	absSrc, err := filepath.Abs(src)
	if err != nil {
		return err
	}

	absDst, err := filepath.Abs(dst)
	if err != nil {
		return err
	}

	srcInfo, err := os.Lstat(absSrc)
	if err != nil {
		return err
	}

	if err := guardOverlap(absSrc, absDst, srcInfo); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(absDst), 0o755); err != nil {
		return err
	}

	if srcInfo.IsDir() {
		return copyTree(ctx, absSrc, absDst, cfg)
	}

	return copyEntry(ctx, absSrc, absDst, filepath.Base(absSrc), srcInfo, treeContext{cfg: cfg})
}

// guardOverlap rejects transfers that would destroy their own source: the
// same path (or same file through different names), and a directory
// destination inside the source tree.
func guardOverlap(absSrc, absDst string, srcInfo fs.FileInfo) error {
	if absSrc == absDst {
		return fmt.Errorf("source and destination are the same path %q", absSrc)
	}

	if dstInfo, err := os.Lstat(absDst); err == nil {
		if os.SameFile(srcInfo, dstInfo) {
			return fmt.Errorf("source %q and destination %q are the same file", absSrc, absDst)
		}
	}

	if srcInfo.IsDir() && pathContains(absSrc, absDst) {
		return fmt.Errorf("destination %q is inside the source tree %q", absDst, absSrc)
	}

	return nil
}

// treeContext carries per-transfer state through the walk.
type treeContext struct {
	cfg invoke.TransferConfig

	// realRoot is the symlink-resolved transfer root, the containment
	// boundary for SymlinkFollow.
	realRoot string
}

// copyTree copies the directory tree rooted at absSrc to absDst. Directory
// modes are recorded during the walk and applied deepest-first afterward,
// so read-only source directories do not block their own contents.
func copyTree(ctx context.Context, absSrc, absDst string, cfg invoke.TransferConfig) error {
	realRoot, err := filepath.EvalSymlinks(absSrc)
	if err != nil {
		return err
	}

	tctx := treeContext{cfg: cfg, realRoot: realRoot}

	type dirMode struct {
		path string
		mode fs.FileMode
	}

	var dirModes []dirMode

	err = filepath.WalkDir(absSrc, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if err := ctx.Err(); err != nil {
			return err
		}

		rel, err := filepath.Rel(absSrc, path)
		if err != nil {
			return err
		}

		target := filepath.Join(absDst, rel)

		info, err := entry.Info()
		if err != nil {
			return err
		}

		if entry.IsDir() {
			if err := makeDir(target); err != nil {
				return err
			}

			dirModes = append(dirModes, dirMode{path: target, mode: info.Mode().Perm()})

			return nil
		}

		return copyEntry(ctx, path, target, rel, info, tctx)
	})
	if err != nil {
		return err
	}

	// Deepest entries first, so restoring a read-only mode on a parent
	// cannot break a child's chmod.
	for i := len(dirModes) - 1; i >= 0; i-- {
		if err := os.Chmod(dirModes[i].path, dirModes[i].mode); err != nil {
			return err
		}
	}

	return nil
}

// makeDir creates target as a directory, merging with an existing one.
func makeDir(target string) error {
	if err := os.Mkdir(target, 0o755); err != nil {
		if info, statErr := os.Stat(target); statErr == nil && info.IsDir() {
			return nil
		}

		return err
	}

	return nil
}

// copyEntry copies one non-directory entry according to its type and the
// transfer's symlink and special-file policy.
func copyEntry(ctx context.Context, src, dst, rel string, info fs.FileInfo, tctx treeContext) error {
	switch {
	case info.Mode().IsRegular():
		return copyFile(ctx, src, dst, rel, info.Mode().Perm(), tctx.cfg)

	case info.Mode()&fs.ModeSymlink != 0:
		return copySymlink(ctx, src, dst, rel, tctx)

	default:
		if tctx.cfg.SkipSpecial {
			return nil
		}

		return fmt.Errorf("unsupported special file %q (%s); use WithSkipSpecial to omit it", src, info.Mode().Type())
	}
}

// copySymlink applies the transfer's symlink policy to one link.
func copySymlink(ctx context.Context, src, dst, rel string, tctx treeContext) error {
	switch tctx.cfg.Symlinks {
	case invoke.SymlinkSkip:
		return nil

	case invoke.SymlinkPreserve:
		linkTarget, err := os.Readlink(src)
		if err != nil {
			return err
		}

		return replaceWithSymlink(linkTarget, dst)

	case invoke.SymlinkFollow:
		resolved, err := filepath.EvalSymlinks(src)
		if err != nil {
			return fmt.Errorf("following symlink %q: %w", src, err)
		}

		if tctx.realRoot != "" && !pathContains(tctx.realRoot, resolved) {
			return fmt.Errorf("symlink %q resolves outside the transfer root: %q", src, resolved)
		}

		info, err := os.Stat(resolved)
		if err != nil {
			return err
		}

		if !info.Mode().IsRegular() {
			return fmt.Errorf("following symlink %q: target %q is not a regular file", src, resolved)
		}

		return copyFile(ctx, resolved, dst, rel, info.Mode().Perm(), tctx.cfg)

	default:
		return fmt.Errorf("unknown symlink policy %d", tctx.cfg.Symlinks)
	}
}

// replaceWithSymlink atomically installs a symlink at dst via a temporary
// name and rename.
func replaceWithSymlink(linkTarget, dst string) error {
	tmp := filepath.Join(filepath.Dir(dst), ".invoke-link-"+filepath.Base(dst)+".tmp")

	_ = os.Remove(tmp)

	if err := os.Symlink(linkTarget, tmp); err != nil {
		return err
	}

	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)

		return err
	}

	return nil
}

// copyFile copies one regular file atomically: the content lands in a
// temporary file in the destination directory, is flushed and given its
// final mode, and only then renamed over dst. A failed or canceled copy
// never corrupts an existing destination.
func copyFile(ctx context.Context, src, dst, rel string, srcMode fs.FileMode, cfg invoke.TransferConfig) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}

	defer func() {
		_ = in.Close()
	}()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	// The entry was a regular file at walk time; re-check on the open
	// handle so a racing replacement (a FIFO, say) cannot stall the
	// copy loop.
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%q changed type during transfer", src)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".invoke-*.tmp")
	if err != nil {
		return err
	}

	if err := writeFile(ctx, tmp, in, rel, info.Size(), srcMode, cfg); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())

		return err
	}

	if err := os.Rename(tmp.Name(), dst); err != nil {
		_ = os.Remove(tmp.Name())

		return err
	}

	return nil
}

// writeFile streams src into the temporary file and finalizes its mode.
func writeFile(ctx context.Context, tmp *os.File, src io.Reader, rel string, total int64, srcMode fs.FileMode, cfg invoke.TransferConfig) error {
	reader := io.Reader(&ctxReader{ctx: ctx, inner: src})

	if cfg.Progress != nil {
		reader = &progressReader{
			inner: reader,
			path:  rel,
			total: total,
			fn:    cfg.Progress,
		}
	}

	if _, err := io.Copy(tmp, reader); err != nil {
		return err
	}

	mode := srcMode
	if cfg.Mode != nil {
		mode = *cfg.Mode
	}

	// Chmod on the handle after creation: umask cannot mask it, and it
	// applies to overwrites via the rename that follows.
	if err := tmp.Chmod(mode); err != nil {
		return err
	}

	if err := tmp.Sync(); err != nil {
		return err
	}

	return tmp.Close()
}

// pathContains reports whether p is root or inside root.
func pathContains(root, p string) bool {
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

// ctxReader fails the next Read once the context is done, so a transfer
// stops promptly on cancellation.
type ctxReader struct {
	ctx   context.Context //nolint:containedctx // Adapter binding one copy loop to its call's context.
	inner io.Reader
}

func (r *ctxReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}

	return r.inner.Read(p)
}

// progressReader reports per-file transfer progress as bytes move.
type progressReader struct {
	inner   io.Reader
	path    string
	current int64
	total   int64
	fn      func(invoke.TransferProgress)
}

func (r *progressReader) Read(p []byte) (int, error) {
	n, err := r.inner.Read(p)
	if n > 0 {
		r.current += int64(n)
		r.fn(invoke.TransferProgress{Path: r.path, Current: r.current, Total: r.total})
	}

	if err != nil && !errors.Is(err, io.EOF) {
		return n, err
	}

	return n, err
}
