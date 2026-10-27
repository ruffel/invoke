package docker

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// placeArrival moves a downloaded entry from staging into place, with
// the rules mergeScript keeps inside the container: a tree merges into
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
	tmp := to + ".invoke-new-" + rand.Text()

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
