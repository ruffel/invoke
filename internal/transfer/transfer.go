// Package transfer implements the copy engine behind every provider's
// Upload and Download: atomic temp-and-rename delivery, tree walking with
// deepest-first directory-mode restoration, symlink and special-file
// policy, per-file progress, and prompt cancellation. A provider plugs an
// [FS] into each side of [Copy] and inherits the semantics the invoketest
// transfer contracts specify, rather than re-implementing them per
// transport.
package transfer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"

	"github.com/ruffel/invoke"
)

// ReadFile is an open source file. Stat re-checks the open handle, so a
// racing type replacement cannot stall the copy loop.
type ReadFile interface {
	io.ReadCloser
	Stat() (fs.FileInfo, error)
}

// WriteFile is an exclusively created destination file being filled.
type WriteFile interface {
	io.WriteCloser
	Chmod(mode fs.FileMode) error
	Sync() error
}

// PathFS is the path algebra of one side of a transfer. Each side applies
// its own separator rules, so a transfer between unlike systems stays
// correct on both.
type PathFS interface {
	Abs(path string) (string, error)
	Join(elem ...string) string
	Dir(path string) string
	Base(path string) string

	// Contains reports whether path is root itself or lies under it.
	Contains(root, path string) bool
}

// MetaFS reads and writes file metadata.
type MetaFS interface {
	Lstat(path string) (fs.FileInfo, error)
	Stat(path string) (fs.FileInfo, error)
	ReadDir(path string) ([]fs.FileInfo, error)
	Mkdir(path string) error
	MkdirAll(path string) error
	Chmod(path string, mode fs.FileMode) error

	// SameFile reports whether two stat results name the same file, when
	// the filesystem can tell; a side that cannot may always report false.
	SameFile(a, b fs.FileInfo) bool
}

// DataFS moves file content and whole entries.
type DataFS interface {
	Open(path string) (ReadFile, error)

	// CreateExclusive creates path for writing, failing if it exists.
	CreateExclusive(path string) (WriteFile, error)

	// Rename moves oldPath over newPath, replacing any existing entry
	// atomically.
	Rename(oldPath, newPath string) error

	Remove(path string) error
}

// LinkFS reads and writes symbolic links.
type LinkFS interface {
	Symlink(target, link string) error
	Readlink(path string) (string, error)

	// Resolve canonicalizes path, following symbolic links.
	Resolve(path string) (string, error)
}

// FS is one side of a transfer. Implementations must be comparable: Copy
// compares its two sides to decide whether same-file overlap guards apply.
type FS interface {
	PathFS
	MetaFS
	DataFS
	LinkFS
}

// Copy copies srcPath on src to dstPath on dst after validating the
// transfer is safe: on a shared filesystem the paths must not alias each
// other, and a directory may not be copied into its own subtree.
func Copy(ctx context.Context, src FS, srcPath string, dst FS, dstPath string, cfg invoke.TransferConfig) error {
	// Checked before anything is created, so a caller whose deadline has
	// already expired never sees success and never leaves a destination
	// root behind.
	if err := ctx.Err(); err != nil {
		return err
	}

	e := endpoints{src: src, dst: dst}

	absSrc, err := src.Abs(srcPath)
	if err != nil {
		return err
	}

	absDst, err := dst.Abs(dstPath)
	if err != nil {
		return err
	}

	srcInfo, err := src.Lstat(absSrc)
	if err != nil {
		return err
	}

	if src == dst {
		if err := e.guardOverlap(absSrc, absDst, srcInfo); err != nil {
			return err
		}
	}

	if err := dst.MkdirAll(dst.Dir(absDst)); err != nil {
		return err
	}

	if srcInfo.IsDir() {
		return e.copyTree(ctx, absSrc, absDst, cfg)
	}

	return e.copyEntry(ctx, absSrc, absDst, src.Base(absSrc), srcInfo, treeContext{cfg: cfg})
}

// endpoints carries the two sides of one transfer through the copy.
type endpoints struct {
	src, dst FS
}

// treeContext carries per-transfer state through the walk.
type treeContext struct {
	cfg invoke.TransferConfig

	// realRoot is the symlink-resolved transfer root, the containment
	// boundary for SymlinkFollow.
	realRoot string
}

// dirMode records a created directory's source mode for deferred
// restoration.
type dirMode struct {
	path string
	mode fs.FileMode
}

// guardOverlap rejects transfers that would destroy their own source: the
// same path (or same file through different names), and a directory
// destination inside the source tree.
func (e endpoints) guardOverlap(absSrc, absDst string, srcInfo fs.FileInfo) error {
	if absSrc == absDst {
		return fmt.Errorf("source and destination are the same path %q", absSrc)
	}

	if dstInfo, err := e.dst.Lstat(absDst); err == nil {
		if e.src.SameFile(srcInfo, dstInfo) {
			return fmt.Errorf("source %q and destination %q are the same file", absSrc, absDst)
		}
	}

	if srcInfo.IsDir() && e.src.Contains(absSrc, absDst) {
		return fmt.Errorf("destination %q is inside the source tree %q", absDst, absSrc)
	}

	return nil
}

// copyTree copies the directory tree rooted at absSrc to absDst. Directory
// modes are recorded during the walk and applied deepest-first afterward,
// so read-only source directories do not block their own contents.
func (e endpoints) copyTree(ctx context.Context, absSrc, absDst string, cfg invoke.TransferConfig) error {
	realRoot, err := e.src.Resolve(absSrc)
	if err != nil {
		return err
	}

	rootInfo, err := e.src.Stat(absSrc)
	if err != nil {
		return err
	}

	if err := e.makeDir(absDst); err != nil {
		return err
	}

	tctx := treeContext{cfg: cfg, realRoot: realRoot}
	dirModes := []dirMode{{path: absDst, mode: rootInfo.Mode().Perm()}}

	if err := e.walk(ctx, absSrc, absDst, "", &dirModes, tctx); err != nil {
		return err
	}

	// Deepest entries first, so restoring a read-only mode on a parent
	// cannot break a child's chmod.
	for i := len(dirModes) - 1; i >= 0; i-- {
		if err := e.dst.Chmod(dirModes[i].path, dirModes[i].mode); err != nil {
			return err
		}
	}

	return nil
}

// walk copies the children of srcDir into dstDir, recording each created
// directory for deferred mode restoration.
func (e endpoints) walk(ctx context.Context, srcDir, dstDir, rel string, dirModes *[]dirMode, tctx treeContext) error {
	entries, err := e.src.ReadDir(srcDir)
	if err != nil {
		return err
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	for _, info := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}

		srcPath := e.src.Join(srcDir, info.Name())
		dstPath := e.dst.Join(dstDir, info.Name())
		entryRel := e.src.Join(rel, info.Name())

		if info.IsDir() {
			if err := e.makeDir(dstPath); err != nil {
				return err
			}

			*dirModes = append(*dirModes, dirMode{path: dstPath, mode: info.Mode().Perm()})

			if err := e.walk(ctx, srcPath, dstPath, entryRel, dirModes, tctx); err != nil {
				return err
			}

			continue
		}

		if err := e.copyEntry(ctx, srcPath, dstPath, entryRel, info, tctx); err != nil {
			return err
		}
	}

	return nil
}

// makeDir creates target as a directory, merging with an existing one.
func (e endpoints) makeDir(target string) error {
	if err := e.dst.Mkdir(target); err != nil {
		if info, statErr := e.dst.Stat(target); statErr == nil && info.IsDir() {
			return nil
		}

		return err
	}

	return nil
}

// copyEntry copies one non-directory entry according to its type and the
// transfer's symlink and special-file policy.
func (e endpoints) copyEntry(ctx context.Context, src, dst, rel string, info fs.FileInfo, tctx treeContext) error {
	switch {
	case info.Mode().IsRegular():
		return e.copyFile(ctx, src, dst, rel, info.Mode().Perm(), tctx.cfg)

	case info.Mode()&fs.ModeSymlink != 0:
		return e.copySymlink(ctx, src, dst, rel, tctx)

	default:
		if tctx.cfg.SkipSpecial {
			return nil
		}

		return fmt.Errorf("unsupported special file %q (%s); use WithSkipSpecial to omit it", src, info.Mode().Type())
	}
}

// copySymlink applies the transfer's symlink policy to one link.
func (e endpoints) copySymlink(ctx context.Context, src, dst, rel string, tctx treeContext) error {
	switch tctx.cfg.Symlinks {
	case invoke.SymlinkSkip:
		return nil

	case invoke.SymlinkPreserve:
		linkTarget, err := e.src.Readlink(src)
		if err != nil {
			return err
		}

		return e.replaceWithSymlink(linkTarget, dst)

	case invoke.SymlinkFollow:
		return e.followSymlink(ctx, src, dst, rel, tctx)

	default:
		return fmt.Errorf("unknown symlink policy %d", tctx.cfg.Symlinks)
	}
}

// followSymlink copies a link's target content in place of the link,
// refusing targets outside the transfer root.
func (e endpoints) followSymlink(ctx context.Context, src, dst, rel string, tctx treeContext) error {
	resolved, err := e.src.Resolve(src)
	if err != nil {
		return fmt.Errorf("following symlink %q: %w", src, err)
	}

	if tctx.realRoot != "" && !e.src.Contains(tctx.realRoot, resolved) {
		return fmt.Errorf("symlink %q resolves outside the transfer root: %q", src, resolved)
	}

	info, err := e.src.Stat(resolved)
	if err != nil {
		return err
	}

	if !info.Mode().IsRegular() {
		return fmt.Errorf("following symlink %q: target %q is not a regular file", src, resolved)
	}

	return e.copyFile(ctx, resolved, dst, rel, info.Mode().Perm(), tctx.cfg)
}

// replaceWithSymlink atomically installs a symlink at dst via a temporary
// name and rename.
func (e endpoints) replaceWithSymlink(linkTarget, dst string) error {
	tmp := e.dst.Join(e.dst.Dir(dst), ".invoke-link-"+e.dst.Base(dst)+".tmp")

	_ = e.dst.Remove(tmp)

	if err := e.dst.Symlink(linkTarget, tmp); err != nil {
		return err
	}

	if err := e.dst.Rename(tmp, dst); err != nil {
		_ = e.dst.Remove(tmp)

		return err
	}

	return nil
}

// copyFile copies one regular file atomically: the content lands in an
// exclusively created temporary file in the destination directory, is
// flushed and given its final mode, and only then renamed over dst. A
// failed or canceled copy never corrupts an existing destination.
func (e endpoints) copyFile(ctx context.Context, src, dst, rel string, srcMode fs.FileMode, cfg invoke.TransferConfig) error {
	in, err := e.src.Open(src)
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

	tmpPath, tmp, err := e.tempFile(dst)
	if err != nil {
		return err
	}

	if err := writeFile(ctx, tmp, in, rel, info.Size(), srcMode, cfg); err != nil {
		_ = tmp.Close()
		_ = e.dst.Remove(tmpPath)

		return err
	}

	if err := e.dst.Rename(tmpPath, dst); err != nil {
		_ = e.dst.Remove(tmpPath)

		return err
	}

	return nil
}

// tempFile exclusively creates a temporary file alongside dst, returning
// its path and open handle.
func (e endpoints) tempFile(dst string) (string, WriteFile, error) {
	suffix, err := randomSuffix()
	if err != nil {
		return "", nil, err
	}

	name := e.dst.Join(e.dst.Dir(dst), ".invoke-"+suffix+".tmp")

	f, err := e.dst.CreateExclusive(name)
	if err != nil {
		return "", nil, err
	}

	return name, f, nil
}

// randomSuffix returns random hex material for a collision-free temp name.
func randomSuffix() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}

	return hex.EncodeToString(buf[:]), nil
}

// writeFile streams src into the temporary file and finalizes its mode.
func writeFile(ctx context.Context, tmp WriteFile, src io.Reader, rel string, total int64, srcMode fs.FileMode, cfg invoke.TransferConfig) error {
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
