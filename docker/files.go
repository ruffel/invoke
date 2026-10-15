package docker

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

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

	if err := e.beginTransfer(ctx, "upload"); err != nil {
		return err
	}

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

	if err := e.mergeRemote(ctx, path.Join(staging, name), dst); err != nil {
		return fmt.Errorf("docker: upload: %w", err)
	}

	return nil
}

// download copies a path out of the container to the local filesystem,
// with the same staging guarantee as Upload.
func (e *Environment) download(ctx context.Context, remotePath, localPath string, opts ...invoke.TransferOption) error {
	cfg := invoke.NewTransferConfig(opts...)

	if err := e.beginTransfer(ctx, "download"); err != nil {
		return err
	}

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

// beginTransfer performs the checks every transfer shares.
func (e *Environment) beginTransfer(ctx context.Context, op string) error {
	if err := e.checkOpen(op); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("docker: %s: %w", op, err)
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

// mergeRemote moves a staged arrival into place inside the container,
// merging a tree into an existing directory as the transfer engine does.
//
// A tree lands in an existing directory by merging into it, as cp, scp
// and rsync do: each file replaces its namesake, and what only the
// destination holds is left alone. A directory already there is merged
// into and given the source's mode; a symbolic link where the tree has a
// directory is refused rather than written through, and a file where it
// has one, or a directory where it has a file, is an error rather than
// something to delete. The root may be a link to a directory, which is
// the ordinary way to point a stable name at a release.
//
// Every entry lands by way of a temporary name beside it, then one
// rename onto the name it was asked for. Staging shares the root's
// parent, but the tree can cross onto another filesystem below it, a
// mounted child directory or a root linked elsewhere, and there mv falls
// back to copying: the copy goes to the temporary name, which nothing
// reads, and the real name still appears in one step. A symbolic link
// standing where a file arrives is moved aside first and put back if the
// file cannot follow it in: mv given a link to a directory moves into it,
// -T or not, so the link has to be out of the way.
func (e *Environment) mergeRemote(ctx context.Context, src, dst string) error {
	// A refusal is reported on stdout, which is what runRaw hands back.
	script := `fail() { printf '%s\n' "$*"; exit 1; }
suffix=$3

land() {
	tmp="$2.invoke-new-$suffix"
	if ! mv -- "$1" "$tmp"; then rm -rf -- "$tmp"; fail "moving $1 beside $2 failed"; fi
	if [ -L "$2" ]; then
		aside="$2.invoke-old-$suffix"
		if ! mv -- "$2" "$aside"; then rm -rf -- "$tmp"; fail "setting $2 aside failed"; fi
		if ! mv -T -- "$tmp" "$2"; then
			mv -- "$aside" "$2"
			rm -rf -- "$tmp"
			fail "moving into $2 failed"
		fi
		rm -f -- "$aside"
		return
	fi
	if ! mv -fT -- "$tmp" "$2"; then rm -rf -- "$tmp"; fail "moving into $2 failed"; fi
}

place() {
	if [ -d "$2" ] && [ ! -L "$2" ]; then fail "destination $2 is a directory"; fi
	land "$1" "$2"
}

merge() {
	mode=$(stat -c %a "$1") || exit 1
	chmod u+rwx "$1" || exit 1
	for e in "$1"/* "$1"/.[!.]* "$1"/..?*; do
		[ -e "$e" ] || [ -L "$e" ] || continue
		t="$2/${e##*/}"
		if [ -d "$e" ] && [ ! -L "$e" ]; then
			if [ -L "$t" ]; then fail "destination $t is a symbolic link; refusing to write the transferred tree through it"; fi
			if [ -d "$t" ]; then
				( merge "$e" "$t" ) || exit 1
			elif [ -e "$t" ]; then
				fail "destination $t is not a directory"
			else
				( land "$e" "$t" ) || exit 1
			fi
		else
			( place "$e" "$t" ) || exit 1
		fi
	done
	chmod "$mode" "$2" || exit 1
}

if [ -d "$1" ] && [ ! -L "$1" ]; then
	if [ -d "$2" ]; then merge "$1" "$2"; exit; fi
	if [ -e "$2" ] || [ -L "$2" ]; then fail "destination $2 exists and is not a directory"; fi
	land "$1" "$2"
else
	place "$1" "$2"
fi`

	out, code, err := e.runRaw(ctx, []string{"sh", "-c", script, "sh", src, dst, randomSuffix()})
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

// placeArrival moves a downloaded entry from staging into place, with
// the rules mergeRemote keeps inside the container: a tree merges into
// an existing directory, each entry landing by its own rename, and
// nothing the destination alone holds is removed.
func placeArrival(ctx context.Context, src, dst string) error {
	return placer{rename: os.Rename}.arrive(ctx, src, dst)
}

// placer moves staged entries into place. rename is os.Rename outside
// tests, which stand in for a filesystem boundary with it.
type placer struct {
	rename func(from, to string) error
}

func (p placer) arrive(ctx context.Context, src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}

	if !info.IsDir() {
		return p.placeFile(src, dst)
	}

	// Followed: the root may be a link to a directory.
	existing, err := os.Stat(dst)
	if errors.Is(err, fs.ErrNotExist) {
		return p.land(src, dst)
	}

	if err != nil {
		return err
	}

	if !existing.IsDir() {
		return fmt.Errorf("destination %q exists and is not a directory", dst)
	}

	return p.mergeDir(ctx, src, dst, info.Mode().Perm())
}

// placeFile lands a file or link over whatever file or link stands at
// dst, refusing to replace a directory with it.
func (p placer) placeFile(src, dst string) error {
	if existing, err := os.Lstat(dst); err == nil && existing.IsDir() {
		return fmt.Errorf("destination %q is a directory", dst)
	}

	return p.land(src, dst)
}

// land moves from into place at to, by way of a temporary name beside
// it, then one rename onto it, which replaces a file or link there.
//
// Staging shares the root's parent, but a tree can cross onto another
// filesystem below it, and there a rename fails. The move to the
// temporary name copies instead, to a name nothing reads, and the name
// asked for still appears in one step.
func (p placer) land(from, to string) error {
	tmp := to + ".invoke-new-" + randomSuffix()

	if err := p.moveAcross(from, tmp); err != nil {
		_ = os.RemoveAll(tmp)

		return err
	}

	if err := os.Rename(tmp, to); err != nil {
		_ = os.RemoveAll(tmp)

		return err
	}

	return nil
}

// moveAcross renames from to to, copying instead when the two lie on
// different filesystems.
func (p placer) moveAcross(from, to string) error {
	err := p.rename(from, to)
	if !errors.Is(err, syscall.EXDEV) {
		return err
	}

	if err := copyEntry(from, to); err != nil {
		return err
	}

	return os.RemoveAll(from)
}

// mergeDir merges the staged directory src into the existing directory
// dst, then gives dst the source's mode.
//
// The staged directory is made writable first: its mode came from the
// archive, and moving its entries out needs to write it. It is discarded
// afterwards, and its own mode is what dst is given.
func (p placer) mergeDir(ctx context.Context, src, dst string, mode fs.FileMode) error {
	if err := os.Chmod(src, 0o700); err != nil { //nolint:gosec // A directory, which needs its execute bit to be walked.
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		// A merge is many renames, and a canceled one stops between them:
		// what has landed stays whole, and the rest is not delivered.
		if err := ctx.Err(); err != nil {
			return err
		}

		if err := p.mergeEntry(ctx, filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
			return err
		}
	}

	return os.Chmod(dst, mode)
}

// mergeEntry places one entry of a staged directory beneath an existing
// directory.
func (p placer) mergeEntry(ctx context.Context, from, to string) error {
	info, err := os.Lstat(from)
	if err != nil {
		return err
	}

	if !info.IsDir() {
		return p.placeFile(from, to)
	}

	existing, err := os.Lstat(to)
	if errors.Is(err, fs.ErrNotExist) {
		return p.land(from, to)
	}

	if err != nil {
		return err
	}

	if existing.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("destination %q is a symbolic link; refusing to write the transferred tree through it", to)
	}

	if !existing.IsDir() {
		return fmt.Errorf("destination %q is not a directory", to)
	}

	return p.mergeDir(ctx, from, to, info.Mode().Perm())
}

// copyEntry copies a staged file, link or tree to to, keeping modes, for
// a move that cannot be a rename.
func copyEntry(from, to string) error {
	info, err := os.Lstat(from)
	if err != nil {
		return err
	}

	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(from)
		if err != nil {
			return err
		}

		return os.Symlink(target, to)

	case info.IsDir():
		if err := os.Mkdir(to, 0o700); err != nil {
			return err
		}

		entries, err := os.ReadDir(from)
		if err != nil {
			return err
		}

		for _, entry := range entries {
			if err := copyEntry(filepath.Join(from, entry.Name()), filepath.Join(to, entry.Name())); err != nil {
				return err
			}
		}

		return os.Chmod(to, info.Mode().Perm())

	default:
		return copyFile(from, to, info.Mode().Perm())
	}
}

// copyFile copies one regular file, applying mode after the write so the
// umask cannot narrow it.
func copyFile(from, to string, mode fs.FileMode) error {
	in, err := os.Open(from) //nolint:gosec // A path inside this transfer's own staging.
	if err != nil {
		return err
	}

	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // Beside the destination the caller named.
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()

		return err
	}

	if err := out.Chmod(mode); err != nil {
		_ = out.Close()

		return err
	}

	return out.Close()
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
