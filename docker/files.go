package docker

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/ruffel/invoke"
)

// Upload copies a local file or directory tree into the container.
//
// The archive is unpacked into a staging directory and moved into place
// only once it has arrived whole, so a transfer that fails or is canceled
// part-way leaves an existing destination exactly as it was. The daemon's
// own unpacking offers no such guarantee: it writes as the bytes arrive.
func (e *Environment) Upload(ctx context.Context, localPath, remotePath string, opts ...invoke.TransferOption) error {
	cfg := invoke.NewTransferConfig(opts...)

	if err := e.beginTransfer(ctx, "upload"); err != nil {
		return err
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

	staging := path.Join(dstDir, ".invoke-xfer-"+randomSuffix())
	if err := e.makeRemoteDir(ctx, staging); err != nil {
		return fmt.Errorf("docker: upload: %w", err)
	}

	//nolint:contextcheck // Cleanup detaches by design; see removeRemote.
	defer e.removeRemote(staging)

	name := path.Base(dst)

	if err := e.sendArchive(ctx, staging, localPath, name, cfg); err != nil {
		return fmt.Errorf("docker: upload: %w", err)
	}

	aside := dst + ".invoke-old-" + randomSuffix()

	if err := e.moveRemote(ctx, path.Join(staging, name), dst, aside); err != nil {
		return fmt.Errorf("docker: upload: %w", err)
	}

	return nil
}

// Download copies a path out of the container to the local filesystem,
// with the same staging guarantee as Upload.
func (e *Environment) Download(ctx context.Context, remotePath, localPath string, opts ...invoke.TransferOption) error {
	cfg := invoke.NewTransferConfig(opts...)

	if err := e.beginTransfer(ctx, "download"); err != nil {
		return err
	}

	src, err := normalizeRemote(remotePath)
	if err != nil {
		return fmt.Errorf("docker: download: %w", err)
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

	if err := replacePath(arrived, dst); err != nil {
		return fmt.Errorf("docker: download: %w", err)
	}

	return nil
}

// beginTransfer performs the checks every transfer shares.
func (e *Environment) beginTransfer(ctx context.Context, op string) error {
	if err := e.checkOpen(op); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("docker: %s: %w", op, err)
	}

	if !e.hasShell {
		return fmt.Errorf("docker: %s: container has no shell to stage the transfer: %w", op, invoke.ErrNotSupported)
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

	copyErr := e.client.CopyToContainer(ctx, e.id, dstDir, reader, container.CopyToContainerOptions{})

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
	body, _, err := e.client.CopyFromContainer(ctx, e.id, src)
	if err != nil {
		return classifyTransfer(err)
	}

	defer func() { _ = body.Close() }()

	return extractTree(ctx, tar.NewReader(body), dst, cfg)
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

// moveRemote replaces dst with src inside the container.
func (e *Environment) moveRemote(ctx context.Context, src, dst, aside string) error {
	// Staging shares the destination's directory, so each mv here is a
	// rename on one filesystem. An existing destination is set aside rather
	// than removed, and only cleared once the new one is in its place, so a
	// move that fails leaves the original recoverable and restored — never
	// a destination deleted ahead of a replacement that did not arrive.
	script := `if [ -e "$2" ] || [ -L "$2" ]; then
	mv -f -- "$2" "$3" || exit 1
	if ! mv -f -- "$1" "$2"; then
		mv -f -- "$3" "$2"
		exit 1
	fi
	rm -rf -- "$3"
else
	mv -f -- "$1" "$2" || exit 1
fi`

	_, code, err := e.runRaw(ctx, []string{"sh", "-c", script, "sh", src, dst, aside})
	if err != nil {
		return err
	}

	if code != 0 {
		return fmt.Errorf("moving the transfer into place at %q failed", dst)
	}

	return nil
}

// removeRemote deletes a staging directory, best effort: the transfer has
// already succeeded or failed on its own terms by this point.
func (e *Environment) removeRemote(dir string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), e.cfg.timeout())
	defer cancel()

	// Best effort: the transfer has already succeeded or failed on its
	// own terms, and a leftover directory under /tmp is not worth
	// reporting in its place.
	if _, _, err := e.runRaw(ctx, []string{"rm", "-rf", "--", dir}); err != nil {
		return
	}
}

// replacePath moves src onto dst, replacing whatever is there.
func replacePath(src, dst string) error {
	if info, err := os.Lstat(dst); err == nil && info.IsDir() {
		if err := os.RemoveAll(dst); err != nil {
			return err
		}
	}

	return os.Rename(src, dst)
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

// randomSuffix returns hex material for a name that will not collide with
// a concurrent command's.
func randomSuffix() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "fallback"
	}

	return hex.EncodeToString(buf[:])
}
