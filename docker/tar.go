package docker

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/internal/transfer"
)

// writeTree writes the tree rooted at src into an archive, naming its top
// entry base, with the transfer's symlink, special-file and mode policy
// applied as it goes.
//
// Ownership is deliberately not carried: the host's numeric ids mean
// nothing inside a container, and copying them would hand files to
// whichever unrelated account happens to hold that id there.
func writeTree(ctx context.Context, tw *tar.Writer, src, base string, cfg invoke.TransferConfig) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}

	if !info.IsDir() {
		return writeEntry(ctx, tw, src, base, base, info, cfg, src)
	}

	if err := writeDirHeader(tw, base, info, cfg); err != nil {
		return err
	}

	return walkTree(ctx, tw, src, base, base, src, cfg)
}

// walkTree writes the children of dir, which is carried in the archive
// under prefix.
func walkTree(ctx context.Context, tw *tar.Writer, dir, prefix, base, root string, cfg invoke.TransferConfig) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}

		srcPath := filepath.Join(dir, entry.Name())
		name := path.Join(prefix, entry.Name())
		rel := strings.TrimPrefix(name, base+"/")

		if info.IsDir() {
			if err := writeDirHeader(tw, name, info, cfg); err != nil {
				return err
			}

			if err := walkTree(ctx, tw, srcPath, name, base, root, cfg); err != nil {
				return err
			}

			continue
		}

		if err := writeEntry(ctx, tw, srcPath, name, rel, info, cfg, root); err != nil {
			return err
		}
	}

	return nil
}

// writeEntry writes one non-directory entry according to the shared
// classification of its type and the transfer's options.
func writeEntry(
	ctx context.Context,
	tw *tar.Writer,
	srcPath, name, rel string,
	info fs.FileInfo,
	cfg invoke.TransferConfig,
	root string,
) error {
	action, err := transfer.Classify(srcPath, info.Mode(), cfg)
	if err != nil {
		return err
	}

	switch action {
	case transfer.ActionCopyContent:
		return writeFile(ctx, tw, srcPath, name, rel, info, cfg)

	case transfer.ActionPreserveLink:
		return writeSymlink(tw, srcPath, name, info, cfg)

	case transfer.ActionFollowLink:
		return writeFollowedLink(ctx, tw, srcPath, name, rel, cfg, root)

	case transfer.ActionSkip:
		return nil

	default:
		return fmt.Errorf("unknown transfer action %d", action)
	}
}

// writeDirHeader records a directory and its mode, so the destination
// does not inherit a default the source never had.
func writeDirHeader(tw *tar.Writer, name string, info fs.FileInfo, cfg invoke.TransferConfig) error {
	return tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir,
		Name:     name + "/",
		Mode:     int64(transfer.EffectiveMode(info.Mode(), cfg).Perm()),
		ModTime:  info.ModTime(),
	})
}

// writeFile copies one regular file's bytes into the archive, reporting
// progress as they move.
func writeFile(
	ctx context.Context,
	tw *tar.Writer,
	srcPath, name, rel string,
	info fs.FileInfo,
	cfg invoke.TransferConfig,
) error {
	file, err := os.Open(srcPath)
	if err != nil {
		return err
	}

	defer func() { _ = file.Close() }()

	// The entry was a regular file when it was listed; re-check on the
	// open handle so a racing replacement cannot stall the copy.
	opened, err := file.Stat()
	if err != nil {
		return err
	}

	if !opened.Mode().IsRegular() {
		return fmt.Errorf("%q changed type during transfer", srcPath)
	}

	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     name,
		Size:     opened.Size(),
		Mode:     int64(transfer.EffectiveMode(info.Mode(), cfg).Perm()),
		ModTime:  info.ModTime(),
	}); err != nil {
		return err
	}

	var reader io.Reader = &ctxReader{ctx: ctx, inner: file}
	if cfg.Progress != nil {
		reader = &progressReader{inner: reader, path: rel, total: opened.Size(), fn: cfg.Progress}
	}

	written, err := io.Copy(tw, reader)
	if err != nil {
		return err
	}

	// The header has committed to a size; a short read would leave the
	// archive out of step with it, so it is reported rather than shipped.
	if written != opened.Size() {
		return fmt.Errorf("%q changed size during transfer: wrote %d of %d bytes", srcPath, written, opened.Size())
	}

	return nil
}

// writeSymlink records a link as a link, including one whose target does
// not exist.
func writeSymlink(tw *tar.Writer, srcPath, name string, info fs.FileInfo, cfg invoke.TransferConfig) error {
	target, err := os.Readlink(srcPath)
	if err != nil {
		return err
	}

	return tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeSymlink,
		Name:     name,
		Linkname: target,
		Mode:     int64(transfer.EffectiveMode(info.Mode(), cfg).Perm()),
		ModTime:  info.ModTime(),
	})
}

// writeFollowedLink copies the content a link resolves to, refusing a
// target outside the transfer root.
func writeFollowedLink(
	ctx context.Context,
	tw *tar.Writer,
	srcPath, name, rel string,
	cfg invoke.TransferConfig,
	root string,
) error {
	resolved, err := filepath.EvalSymlinks(srcPath)
	if err != nil {
		return fmt.Errorf("following symlink %q: %w", srcPath, err)
	}

	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}

	if err := transfer.CheckFollowTarget(srcPath, resolved, realRoot, containsPath); err != nil {
		return err
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return err
	}

	if !info.Mode().IsRegular() {
		return fmt.Errorf("following symlink %q: target %q is not a regular file", srcPath, resolved)
	}

	return writeFile(ctx, tw, resolved, name, rel, info, cfg)
}

// containsPath reports whether p is root or lies under it, using host
// path rules.
func containsPath(root, p string) bool {
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

// ctxReader fails the next read once the context is done, so a transfer
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
