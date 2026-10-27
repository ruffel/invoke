package docker

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/moby/moby/client"
	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/internal/lifetime"
)

// Upload copies a local file or directory tree into the container. See
// upload; Close stops it, and it then fails wrapping invoke.ErrClosed.
func (e *Environment) Upload(ctx context.Context, localPath, remotePath string, opts ...invoke.TransferOption) error {
	return e.transfer(ctx, "upload", func(ctx context.Context) error {
		return e.upload(ctx, localPath, remotePath, opts...)
	})
}

// Download copies a path out of the container. See download; Close stops
// it as it does an Upload.
func (e *Environment) Download(ctx context.Context, remotePath, localPath string, opts ...invoke.TransferOption) error {
	return e.transfer(ctx, "download", func(ctx context.Context) error {
		return e.download(ctx, remotePath, localPath, opts...)
	})
}

// transfer runs one transfer for the life of the Environment. Close
// cancels it and waits for it, staging cleanup included, before the
// client goes, so nothing it started reaches the daemon after Close has
// returned.
func (e *Environment) transfer(ctx context.Context, op string, run func(context.Context) error) error {
	if err := e.checkOpen(op); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("docker: %s: %w", op, err)
	}

	opCtx, end, err := e.life.Enter(ctx)
	if err != nil {
		return fmt.Errorf("docker: %s: %w", op, err)
	}

	defer end()

	err = run(opCtx)

	// Asked even of a copy that finished: Close stopped it, whether or
	// not its last bytes had already been written.
	if lifetime.Ended(opCtx) {
		return fmt.Errorf("docker: %s: stopped by Close: %w", op, invoke.ErrClosed)
	}

	return err
}

// upload copies a local file or directory tree into the container.
//
// The archive is unpacked into a staging directory and merged into place
// only once it has arrived whole, so a transfer that fails or is canceled
// while the archive is in flight leaves an existing destination exactly as
// it was. The daemon's own unpacking offers no such guarantee: it writes
// as the bytes arrive. The merge itself renames one entry at a time, as
// the engine's does.
func (e *Environment) upload(ctx context.Context, localPath, remotePath string, opts ...invoke.TransferOption) error {
	cfg := invoke.NewTransferConfig(opts...)

	// Staging and the move into place run in the container, through its
	// shell. Download needs neither: the archive is unpacked and placed
	// on this side.
	if !e.hasShell {
		return fmt.Errorf("docker: upload: container has no shell to stage the transfer: %w", invoke.ErrNotSupported)
	}

	dst, err := normalizeRemote(remotePath)
	if err != nil {
		return fmt.Errorf("docker: upload: %w", err)
	}

	if _, err := os.Lstat(localPath); err != nil {
		return fmt.Errorf("docker: upload: %w", err)
	}

	// Stage inside the destination's own directory rather than a fixed
	// path like /tmp, so the move into place is a rename on one filesystem
	// — atomic, and never a copy across a boundary that could fail with
	// the destination already gone.
	dstDir := path.Dir(dst)
	if err := e.makeRemoteDir(ctx, dstDir); err != nil {
		return fmt.Errorf("docker: upload: %w", err)
	}

	staging := path.Join(dstDir, ".invoke-xfer-"+rand.Text())
	if err := e.makeRemoteDir(ctx, staging); err != nil {
		return fmt.Errorf("docker: upload: %w", err)
	}

	//nolint:contextcheck // Cleanup detaches by design; see removeRemote.
	defer e.removeRemote(staging)

	name := path.Base(dst)

	if err := e.sendArchive(ctx, staging, localPath, name, cfg); err != nil {
		return fmt.Errorf("docker: upload: %w", err)
	}

	if err := e.mergeRemote(ctx, path.Join(staging, name), dst); err != nil {
		return fmt.Errorf("docker: upload: %w", err)
	}

	return nil
}

// download copies a path out of the container to the local filesystem,
// with the same staging guarantee as Upload.
func (e *Environment) download(ctx context.Context, remotePath, localPath string, opts ...invoke.TransferOption) error {
	cfg := invoke.NewTransferConfig(opts...)

	src, err := normalizeRemote(remotePath)
	if err != nil {
		return fmt.Errorf("docker: download: %w", err)
	}

	// A path that is itself a link is followed as the other providers
	// follow one, before anything is copied: the daemon resolves it, and
	// what is copied is what it leads to, as docker cp -L does. Links
	// inside a tree are settled once it has arrived.
	if cfg.Symlinks == invoke.SymlinkFollow {
		stat, err := e.client.ContainerStatPath(ctx, e.id, client.ContainerStatPathOptions{Path: src})
		if err != nil {
			return fmt.Errorf("docker: download: %w", classifyTransfer(err))
		}

		if stat.Stat.Mode&os.ModeSymlink != 0 && stat.Stat.LinkTarget != "" {
			src = path.Clean(stat.Stat.LinkTarget)
		}
	}

	dst, err := filepath.Abs(localPath)
	if err != nil {
		return fmt.Errorf("docker: download: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("docker: download: %w", err)
	}

	staging, err := os.MkdirTemp(filepath.Dir(dst), ".invoke-xfer-")
	if err != nil {
		return fmt.Errorf("docker: download: %w", err)
	}

	defer func() { _ = os.RemoveAll(staging) }()

	if err := e.receiveArchive(ctx, src, staging, cfg); err != nil {
		return fmt.Errorf("docker: download: %w", err)
	}

	// The archive names its top entry after the remote path's own base,
	// which need not match what the caller asked the copy to be called.
	arrived := filepath.Join(staging, path.Base(src))

	// Nothing reaches the destination for a download that was canceled
	// while it was being settled.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("docker: download: %w", err)
	}

	if err := placeArrival(ctx, arrived, dst); err != nil {
		return fmt.Errorf("docker: download: %w", err)
	}

	return nil
}

// sendArchive streams a tree into the container as an archive.
//
// The archive is produced on a pipe the daemon reads from, so a failure
// while building it has to be handed to the reader. Every such error is
// wrapped before it crosses: the daemon's transport compares what it
// receives, and a bare error value that does not support comparison
// brings the process down rather than failing the call.
func (e *Environment) sendArchive(
	ctx context.Context,
	dstDir, localPath, name string,
	cfg invoke.TransferConfig,
) error {
	reader, writer := io.Pipe()
	built := make(chan error, 1)

	go func() {
		tw := tar.NewWriter(writer)

		err := writeTree(ctx, tw, localPath, name, cfg)
		if err == nil {
			err = tw.Close()
		}

		if err != nil {
			_ = writer.CloseWithError(fmt.Errorf("building the archive: %w", err))
		} else {
			_ = writer.Close()
		}

		built <- err
	}()

	_, copyErr := e.client.CopyToContainer(ctx, e.id, client.CopyToContainerOptions{DestinationPath: dstDir, Content: reader})

	// Unblock the builder if the daemon stopped reading early.
	_ = reader.CloseWithError(errClosedEarly)

	buildErr := <-built

	// The two failures are usually one: a source the archive cannot carry
	// ends the stream, and the daemon then reports a truncated archive.
	// The builder's reason is the useful one — unless it is only the
	// closing above, which means the daemon failed on its own account.
	if buildErr != nil && !errors.Is(buildErr, io.ErrClosedPipe) && !errors.Is(buildErr, errClosedEarly) {
		return buildErr
	}

	if copyErr != nil {
		return classifyTransfer(copyErr)
	}

	if buildErr != nil {
		return buildErr
	}

	return nil
}

// errClosedEarly marks the pipe being closed because the daemon stopped
// reading, which is a consequence of some other failure rather than one.
var errClosedEarly = errors.New("archive no longer needed")

// receiveArchive streams a path out of the container and unpacks it.
func (e *Environment) receiveArchive(ctx context.Context, src, dst string, cfg invoke.TransferConfig) error {
	res, err := e.client.CopyFromContainer(ctx, e.id, client.CopyFromContainerOptions{SourcePath: src})
	if err != nil {
		return classifyTransfer(err)
	}

	defer func() { _ = res.Content.Close() }()

	return extractTree(ctx, tar.NewReader(res.Content), dst, cfg,
		linkSettler(filepath.Join(dst, path.Base(src)), src, cfg))
}

// normalizeRemote renders a container path in a single form.
//
// A trailing separator makes a destination ambiguous — "into this
// directory" or "as this name" — so it is rejected rather than guessed
// at, which is how a transfer ends up somewhere the caller did not mean.
func normalizeRemote(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("remote path is empty")
	}

	if !path.IsAbs(p) {
		return "", fmt.Errorf("remote path %q must be absolute", p)
	}

	if len(p) > 1 && strings.HasSuffix(p, "/") {
		return "", fmt.Errorf("remote path %q must not end in a separator: "+
			"name the destination itself, not the directory to place it in", p)
	}

	return path.Clean(p), nil
}

// makeRemoteDir creates a directory and its parents in the container.
func (e *Environment) makeRemoteDir(ctx context.Context, dir string) error {
	_, code, err := e.runRaw(ctx, []string{"mkdir", "-p", dir})
	if err != nil {
		return err
	}

	if code != 0 {
		return fmt.Errorf("creating %q in the container failed", dir)
	}

	return nil
}

// mergeRemote moves a staged arrival into place inside the container, by
// the rules mergeScript sets out, and reports the refusal it prints.
func (e *Environment) mergeRemote(ctx context.Context, src, dst string) error {
	out, code, err := e.runRaw(ctx, []string{"sh", "-c", mergeScript, "sh", src, dst, rand.Text()})
	if err != nil {
		return err
	}

	if code != 0 {
		if msg := strings.TrimSpace(out); msg != "" {
			return fmt.Errorf("moving the transfer into place at %q failed: %s", dst, msg)
		}

		return fmt.Errorf("moving the transfer into place at %q failed", dst)
	}

	return nil
}

// removeRemote deletes a staging directory, best effort and detached from
// any caller context: the transfer has already succeeded or failed on its
// own terms, and a leftover directory is not worth reporting in its
// place.
func (e *Environment) removeRemote(dir string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), e.cfg.timeout())
	defer cancel()

	if _, _, err := e.runRaw(ctx, []string{"rm", "-rf", "--", dir}); err != nil {
		return
	}
}

// classifyTransfer folds a daemon failure into the package taxonomy.
func classifyTransfer(err error) error {
	msg := err.Error()

	switch {
	case strings.Contains(msg, "no such file or directory"),
		strings.Contains(msg, "Could not find the file"):
		return fmt.Errorf("%s: %w", msg, invoke.ErrNotFound)

	default:
		return &invoke.TransportError{Op: "archive", Err: err}
	}
}
