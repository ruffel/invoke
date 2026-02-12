package local

import (
	"context"
	"errors"
	"fmt"
	"os"

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

	if cfg.UID != 0 || cfg.GID != 0 {
		return fmt.Errorf("owner options are unsupported by local provider: %w", invoke.ErrNotSupported)
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
