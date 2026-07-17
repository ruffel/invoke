package transfer

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// HostFS is the [FS] for the host's own filesystem — the local side of
// every provider's Upload and Download, and both sides of the local
// provider's.
type HostFS struct{}

var _ FS = HostFS{}

// Abs makes path absolute against the working directory.
func (HostFS) Abs(path string) (string, error) {
	return filepath.Abs(path)
}

// Join joins path elements with the host separator.
func (HostFS) Join(elem ...string) string {
	return filepath.Join(elem...)
}

// Dir returns all but the last element of path.
func (HostFS) Dir(path string) string {
	return filepath.Dir(path)
}

// Base returns the last element of path.
func (HostFS) Base(path string) string {
	return filepath.Base(path)
}

// Contains reports whether path is root itself or lies under it.
func (HostFS) Contains(root, path string) bool {
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

// Lstat stats path without following a trailing symlink.
func (HostFS) Lstat(path string) (fs.FileInfo, error) {
	return os.Lstat(path)
}

// Stat stats path, following symlinks.
func (HostFS) Stat(path string) (fs.FileInfo, error) {
	return os.Stat(path)
}

// ReadDir lists a directory with lstat-style entry info.
func (HostFS) ReadDir(path string) ([]fs.FileInfo, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}

	infos := make([]fs.FileInfo, 0, len(entries))

	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}

		infos = append(infos, info)
	}

	return infos, nil
}

// Mkdir creates one directory at the conventional default mode; walked
// directories are chmod'd to their source's real mode afterward.
func (HostFS) Mkdir(path string) error {
	return os.Mkdir(path, 0o755)
}

// MkdirAll creates missing parent directories at the default mode.
func (HostFS) MkdirAll(path string) error {
	return os.MkdirAll(path, 0o755)
}

// Chmod sets a path's permission bits.
func (HostFS) Chmod(path string, mode fs.FileMode) error {
	return os.Chmod(path, mode)
}

// SameFile reports whether two stat results name the same file.
func (HostFS) SameFile(a, b fs.FileInfo) bool {
	return os.SameFile(a, b)
}

// Open opens path for reading.
func (HostFS) Open(path string) (ReadFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	return f, nil
}

// CreateExclusive creates path for writing, failing if it exists.
func (HostFS) CreateExclusive(path string) (WriteFile, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}

	return f, nil
}

// Rename moves oldPath over newPath atomically.
func (HostFS) Rename(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}

// Remove deletes one entry.
func (HostFS) Remove(path string) error {
	return os.Remove(path)
}

// Symlink creates link pointing at target.
func (HostFS) Symlink(target, link string) error {
	return os.Symlink(target, link)
}

// Readlink returns the target of a symlink.
func (HostFS) Readlink(path string) (string, error) {
	return os.Readlink(path)
}

// Resolve canonicalizes path, following symbolic links.
func (HostFS) Resolve(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}
