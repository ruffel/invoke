package local

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/ruffel/invoke"
)

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
	ctx    context.Context
	reader io.Reader
}

func (cr *contextReader) Read(p []byte) (int, error) {
	if cr.ctx.Err() != nil {
		return 0, cr.ctx.Err()
	}

	return cr.reader.Read(p)
}
