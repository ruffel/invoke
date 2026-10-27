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
	return e.copy(ctx, "upload", localPath, remotePath, opts)
}

// Download copies a file or directory tree between local paths, with the
// same semantics as Upload.
func (e *Environment) Download(ctx context.Context, remotePath, localPath string, opts ...invoke.TransferOption) error {
	return e.copy(ctx, "download", remotePath, localPath, opts)
}

// copy runs one transfer for the life of the Environment: Close stops it
// and waits for it, and it then fails wrapping invoke.ErrClosed.
func (e *Environment) copy(ctx context.Context, op, src, dst string, opts []invoke.TransferOption) error {
	err := e.life.Run(ctx, func(ctx context.Context) error {
		return transfer.Copy(ctx, transfer.HostFS{}, src, transfer.HostFS{}, dst, invoke.NewTransferConfig(opts...))
	})
	if err != nil {
		return fmt.Errorf("local: %s: %w", op, err)
	}

	return nil
}
