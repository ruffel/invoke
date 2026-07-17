package local

import (
	"context"
	"fmt"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/internal/transfer"
)

// Upload copies a file or directory tree between local paths. On this
// provider Upload and Download are the same operation; both exist so the
// Environment contract holds regardless of target.
func (e *Environment) Upload(ctx context.Context, localPath, remotePath string, opts ...invoke.TransferOption) error {
	if err := e.checkOpen("upload"); err != nil {
		return err
	}

	err := transfer.Copy(ctx, transfer.HostFS{}, localPath, transfer.HostFS{}, remotePath, invoke.NewTransferConfig(opts...))
	if err != nil {
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

	err := transfer.Copy(ctx, transfer.HostFS{}, remotePath, transfer.HostFS{}, localPath, invoke.NewTransferConfig(opts...))
	if err != nil {
		return fmt.Errorf("local: download: %w", err)
	}

	return nil
}
