package fake

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/ruffel/invoke"
)

// Upload copies a real host file or tree into the fake target's virtual
// filesystem, with the full transfer semantics providers share: per-file
// atomicity (nothing commits unless the source was read completely),
// modes preserved or forced umask-proof, symlink policies with
// containment, special files erroring by name, and real progress totals.
func (e *Environment) Upload(ctx context.Context, localPath, remotePath string, opts ...invoke.TransferOption) error {
	if err := e.checkOpen("upload"); err != nil {
		return err
	}

	cfg := invoke.NewTransferConfig(opts...)

	absSrc, err := filepath.Abs(localPath)
	if err != nil {
		return fmt.Errorf("fake: upload: %w", err)
	}

	info, err := os.Lstat(absSrc)
	if err != nil {
		return fmt.Errorf("fake: upload: %w", err)
	}

	dst := vfsClean("/", remotePath)
	if err := e.fs.mkdirAll(path.Dir(dst)); err != nil {
		return fmt.Errorf("fake: upload: %w", err)
	}

	if info.IsDir() {
		if err := e.uploadTree(ctx, absSrc, dst, cfg); err != nil {
			return fmt.Errorf("fake: upload: %w", err)
		}

		return nil
	}

	if err := e.uploadEntry(ctx, absSrc, dst, filepath.Base(absSrc), info, cfg, ""); err != nil {
		return fmt.Errorf("fake: upload: %w", err)
	}

	return nil
}

// Download copies a virtual file or tree out to the real host filesystem,
// atomically per file via temp-and-rename.
func (e *Environment) Download(ctx context.Context, remotePath, localPath string, opts ...invoke.TransferOption) error {
	if err := e.checkOpen("download"); err != nil {
		return err
	}

	cfg := invoke.NewTransferConfig(opts...)

	src := vfsClean("/", remotePath)

	node, ok := e.fs.snapshot(src)
	if !ok {
		return fmt.Errorf("fake: download: %q does not exist on the target", remotePath)
	}

	absDst, err := filepath.Abs(localPath)
	if err != nil {
		return fmt.Errorf("fake: download: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(absDst), 0o755); err != nil {
		return fmt.Errorf("fake: download: %w", err)
	}

	if err := e.downloadEntry(ctx, src, absDst, node, cfg); err != nil {
		return fmt.Errorf("fake: download: %w", err)
	}

	return nil
}

func (e *Environment) uploadTree(ctx context.Context, absSrc, dst string, cfg invoke.TransferConfig) error {
	realRoot, err := filepath.EvalSymlinks(absSrc)
	if err != nil {
		return err
	}

	return filepath.WalkDir(absSrc, func(hostPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if err := ctx.Err(); err != nil {
			return err
		}

		rel, err := filepath.Rel(absSrc, hostPath)
		if err != nil {
			return err
		}

		target := path.Join(dst, filepath.ToSlash(rel))

		info, err := entry.Info()
		if err != nil {
			return err
		}

		if entry.IsDir() {
			if err := e.fs.mkdirAll(target); err != nil {
				return err
			}

			e.fs.setDirMode(target, info.Mode().Perm())

			return nil
		}

		return e.uploadEntry(ctx, hostPath, target, filepath.ToSlash(rel), info, cfg, realRoot)
	})
}

func (e *Environment) uploadEntry(ctx context.Context, hostPath, target, rel string, info fs.FileInfo, cfg invoke.TransferConfig, realRoot string) error {
	switch {
	case info.Mode().IsRegular():
		return e.uploadFile(ctx, hostPath, target, rel, info.Mode().Perm(), cfg)

	case info.Mode()&fs.ModeSymlink != 0:
		return e.uploadSymlink(ctx, hostPath, target, rel, cfg, realRoot)

	default:
		if cfg.SkipSpecial {
			return nil
		}

		return fmt.Errorf("unsupported special file %q (%s); use WithSkipSpecial to omit it",
			hostPath, info.Mode().Type())
	}
}

func (e *Environment) uploadSymlink(ctx context.Context, hostPath, target, rel string, cfg invoke.TransferConfig, realRoot string) error {
	switch cfg.Symlinks {
	case invoke.SymlinkSkip:
		return nil

	case invoke.SymlinkPreserve:
		linkTarget, err := os.Readlink(hostPath)
		if err != nil {
			return err
		}

		return e.fs.symlink(linkTarget, target)

	case invoke.SymlinkFollow:
		resolved, err := filepath.EvalSymlinks(hostPath)
		if err != nil {
			return fmt.Errorf("following symlink %q: %w", hostPath, err)
		}

		if realRoot != "" && !hostContains(realRoot, resolved) {
			return fmt.Errorf("symlink %q resolves outside the transfer root: %q", hostPath, resolved)
		}

		info, err := os.Stat(resolved)
		if err != nil {
			return err
		}

		if !info.Mode().IsRegular() {
			return fmt.Errorf("following symlink %q: target %q is not a regular file", hostPath, resolved)
		}

		return e.uploadFile(ctx, resolved, target, rel, info.Mode().Perm(), cfg)

	default:
		return fmt.Errorf("unknown symlink policy %d", cfg.Symlinks)
	}
}

// uploadFile reads a host file completely — in chunks, honoring
// cancellation and progress — and only then commits it to the virtual
// filesystem, which is what makes fake transfers atomic per file.
func (e *Environment) uploadFile(ctx context.Context, hostPath, target, rel string, srcMode fs.FileMode, cfg invoke.TransferConfig) error {
	in, err := os.Open(hostPath)
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

	if !info.Mode().IsRegular() {
		return fmt.Errorf("%q changed type during transfer", hostPath)
	}

	content, err := readAllChunked(ctx, in, rel, info.Size(), cfg.Progress)
	if err != nil {
		return err
	}

	mode := srcMode
	if cfg.Mode != nil {
		mode = *cfg.Mode
	}

	return e.fs.writeFile(target, content, mode)
}

// readAllChunked reads src fully with per-chunk cancellation checks and
// progress reporting.
func readAllChunked(ctx context.Context, src io.Reader, rel string, total int64, progress func(invoke.TransferProgress)) ([]byte, error) {
	var (
		content []byte
		current int64
	)

	buf := make([]byte, copyChunkBytes)

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		n, err := src.Read(buf)
		if n > 0 {
			content = append(content, buf[:n]...)
			current += int64(n)

			if progress != nil {
				progress(invoke.TransferProgress{Path: rel, Current: current, Total: total})
			}
		}

		if err != nil {
			if errors.Is(err, io.EOF) {
				return content, nil
			}

			return nil, err
		}
	}
}

func (e *Environment) downloadEntry(ctx context.Context, src, absDst string, node vnode, cfg invoke.TransferConfig) error {
	switch {
	case node.dir:
		return e.downloadTree(ctx, src, absDst, cfg)

	case node.link != "":
		_ = os.Remove(absDst)

		return os.Symlink(node.link, absDst)

	default:
		return downloadFile(ctx, absDst, src, node, cfg)
	}
}

func (e *Environment) downloadTree(ctx context.Context, src, absDst string, cfg invoke.TransferConfig) error {
	type dirMode struct {
		path string
		mode fs.FileMode
	}

	var deferred []dirMode

	var walk func(vpath, hostPath string, node vnode) error

	walk = func(vpath, hostPath string, node vnode) error {
		if err := ctx.Err(); err != nil {
			return err
		}

		if !node.dir {
			return e.downloadEntry(ctx, vpath, hostPath, node, cfg)
		}

		if err := os.MkdirAll(hostPath, 0o755); err != nil {
			return err
		}

		deferred = append(deferred, dirMode{path: hostPath, mode: node.mode})

		for _, child := range e.fs.children(vpath) {
			childNode, ok := e.fs.snapshot(path.Join(vpath, child))
			if !ok {
				continue
			}

			if err := walk(path.Join(vpath, child), filepath.Join(hostPath, child), childNode); err != nil {
				return err
			}
		}

		return nil
	}

	root, ok := e.fs.snapshot(src)
	if !ok {
		return fmt.Errorf("%q vanished during download", src)
	}

	if err := walk(src, absDst, root); err != nil {
		return err
	}

	for i := len(deferred) - 1; i >= 0; i-- {
		if err := os.Chmod(deferred[i].path, deferred[i].mode); err != nil {
			return err
		}
	}

	return nil
}

// downloadFile writes VFS content to the host atomically: temp file,
// explicit mode, fsync, rename.
func downloadFile(ctx context.Context, absDst, vpath string, node vnode, cfg invoke.TransferConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(absDst), ".invoke-fake-*.tmp")
	if err != nil {
		return err
	}

	cleanup := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())

		return err
	}

	current := int64(0)
	content := node.content

	for len(content) > 0 {
		if err := ctx.Err(); err != nil {
			return cleanup(err)
		}

		chunk := content
		if len(chunk) > copyChunkBytes {
			chunk = chunk[:copyChunkBytes]
		}

		n, err := tmp.Write(chunk)
		if err != nil {
			return cleanup(err)
		}

		content = content[n:]
		current += int64(n)

		if cfg.Progress != nil {
			cfg.Progress(invoke.TransferProgress{
				Path:    path.Base(vpath),
				Current: current,
				Total:   int64(len(node.content)),
			})
		}
	}

	mode := node.mode
	if cfg.Mode != nil {
		mode = *cfg.Mode
	}

	if err := tmp.Chmod(mode); err != nil {
		return cleanup(err)
	}

	if err := tmp.Sync(); err != nil {
		return cleanup(err)
	}

	if err := tmp.Close(); err != nil {
		return cleanup(err)
	}

	if err := os.Rename(tmp.Name(), absDst); err != nil {
		return cleanup(err)
	}

	return nil
}

func hostContains(root, p string) bool {
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}
