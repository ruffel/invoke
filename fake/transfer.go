package fake

import (
	"context"
	"fmt"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/internal/lifetime"
	"github.com/ruffel/invoke/internal/transfer"
)

// Upload copies a host file or tree into the virtual filesystem through
// the shared transfer engine, so it behaves as local's and ssh's do:
// per-file atomicity, merging into an existing directory, modes preserved
// or forced umask-proof, the symlink policy with containment, special
// files refused by name, root-relative progress and the configured
// concurrency. Close stops it, and it then fails wrapping
// invoke.ErrClosed.
func (e *Environment) Upload(ctx context.Context, localPath, remotePath string, opts ...invoke.TransferOption) error {
	return e.transfer(ctx, "upload", func(ctx context.Context) error {
		return transfer.Copy(ctx, transfer.HostFS{}, localPath, vfsFS{e.fs}, remotePath, invoke.NewTransferConfig(opts...))
	})
}

// Download copies a virtual file or tree out to the host, with the same
// semantics as Upload.
func (e *Environment) Download(ctx context.Context, remotePath, localPath string, opts ...invoke.TransferOption) error {
	return e.transfer(ctx, "download", func(ctx context.Context) error {
		return transfer.Copy(ctx, vfsFS{e.fs}, remotePath, transfer.HostFS{}, localPath, invoke.NewTransferConfig(opts...))
	})
}

// transfer runs one transfer for the life of the Environment.
func (e *Environment) transfer(ctx context.Context, op string, run func(context.Context) error) error {
	if err := e.checkOpen(op); err != nil {
		return err
	}

	opCtx, end, err := e.life.Enter(ctx)
	if err != nil {
		return fmt.Errorf("fake: %s: %w", op, err)
	}

	defer end()

	err = run(opCtx)

	// Asked even of a copy that finished: Close stopped it, whether or
	// not its last bytes had already been written.
	if lifetime.Ended(opCtx) {
		return fmt.Errorf("fake: %s: stopped by Close: %w", op, invoke.ErrClosed)
	}

	if err != nil {
		return fmt.Errorf("fake: %s: %w", op, err)
	}

	return nil
}
