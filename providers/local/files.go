package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ruffel/invoke"
)

// Upload copies a local file/dir to the destination path (also local).
func (e *Environment) Upload(ctx context.Context, localPath, remotePath string, opts ...invoke.FileOption) error {
	if e.isClosed() {
		return errors.New("cannot upload files: environment is closed")
	}

	// For local provider, "remote" is just another local path.
	// We handle options generally.
	cfg := invoke.DefaultFileConfig()
	for _, o := range opts {
		o(&cfg)
	}

	info, err := os.Stat(localPath)
	if err != nil {
		return err
	}

	if info.IsDir() {
		if !cfg.Recursive {
			return errors.New("recursive directory upload is disabled by configuration")
		}

		return e.copyDir(ctx, localPath, remotePath, cfg)
	}

	mode := info.Mode()
	if cfg.Permissions != 0 {
		mode = cfg.Permissions
	}

	return e.copyFile(ctx, localPath, remotePath, mode, cfg.Progress)
}

// Download copies a remote file/dir to the destination path (also local).
func (e *Environment) Download(ctx context.Context, remotePath, localPath string, opts ...invoke.FileOption) error {
	if e.isClosed() {
		return errors.New("cannot download files: environment is closed")
	}

	// For local provider, this is symmetric to Upload.
	return e.Upload(ctx, remotePath, localPath, opts...)
}

func (e *Environment) copyDir(ctx context.Context, src, dst string, cfg invoke.FileConfig) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		targetPath := filepath.Join(dst, relPath)

		if err := checkPathTraversal(dst, targetPath); err != nil {
			return err
		}

		if info.IsDir() {
			err := os.MkdirAll(targetPath, info.Mode())
			if err != nil {
				return err
			}

			return nil
		}

		mode := info.Mode()
		if cfg.Permissions != 0 {
			mode = cfg.Permissions
		}

		return e.copyFile(ctx, path, targetPath, mode, cfg.Progress)
	})
}

func (e *Environment) copyFile(ctx context.Context, src, dst string, mode os.FileMode, progress invoke.ProgressFunc) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}

	defer func() { _ = sourceFile.Close() }()

	var size int64
	if info, err := sourceFile.Stat(); err == nil {
		size = info.Size()
	}

	// Ensure parent exists
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	destFile, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_TRUNC, mode)
	if os.IsPermission(err) {
		// Handle read-only files: if OpenFile failed, it might be due to permissions.
		// If the file exists and we are trying to overwrite it, remove it first.
		if _, statErr := os.Stat(dst); statErr == nil {
			if removeErr := os.Remove(dst); removeErr == nil {
				// Try opening again after removal
				destFile, err = os.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_TRUNC, mode)
			}
		}
	}

	if err != nil {
		return err
	}

	defer func() { _ = destFile.Close() }()

	var reader io.Reader = &contextReader{ctx: ctx, reader: sourceFile}
	if progress != nil {
		reader = &progressReader{Reader: reader, total: size, fn: progress}
	}

	_, err = io.Copy(destFile, reader)
	if err != nil {
		return err
	}

	if err := destFile.Sync(); err != nil {
		return err
	}

	return destFile.Close()
}

// progressReader wraps a Reader and reports copy progress.
type progressReader struct {
	io.Reader

	total   int64
	current int64
	fn      invoke.ProgressFunc
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.Reader.Read(p)
	if n > 0 {
		pr.current += int64(n)
		if pr.fn != nil {
			pr.fn(pr.current, pr.total)
		}
	}

	return n, err
}

// contextReader checks cancellation before each read operation.
type contextReader struct {
	ctx    context.Context //nolint:containedctx
	reader io.Reader
}

func (cr *contextReader) Read(p []byte) (int, error) {
	if cr.ctx.Err() != nil {
		return 0, cr.ctx.Err()
	}

	return cr.reader.Read(p)
}

func checkPathTraversal(root, target string) error {
	cleanRoot := filepath.Clean(root)
	cleanTarget := filepath.Clean(target)

	if cleanRoot == cleanTarget {
		return nil
	}

	if !strings.HasPrefix(cleanTarget, cleanRoot+string(os.PathSeparator)) {
		return fmt.Errorf("illegal file path: %s is not within %s", target, root)
	}

	return nil
}
