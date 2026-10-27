package ssh

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/pkg/sftp"
	"github.com/ruffel/invoke/internal/transfer"
)

const (
	// privateMode is the mode a remote file is created with while it is
	// still being written, before the transfer applies its final mode.
	privateMode fs.FileMode = 0o600

	// maxSymlinkHops bounds link expansion during path resolution, so a
	// link cycle on the remote host terminates.
	maxSymlinkHops = 32

	// sftpCopyConcurrency is how many files a tree transfer copies at
	// once over SFTP unless the caller chose otherwise.
	sftpCopyConcurrency = 8
)

// errTooManyLinks reports a symlink cycle or an excessively deep chain.
var errTooManyLinks = errors.New("too many levels of symbolic links")

// sftpFS presents an SFTP session to the shared transfer engine, so the
// provider's transfers are the engine's transfers: the same per-file
// atomicity, merge, symlink policy, containment, progress and concurrency
// that local and the fake get, rather than a re-implementation of them
// that can drift. Remote paths are POSIX regardless of the host platform,
// so the path package — not filepath — does the path algebra.
type sftpFS struct {
	client *sftp.Client
}

var _ transfer.FS = sftpFS{}

// Abs makes p absolute against the session's working directory (the
// login user's home, for OpenSSH).
func (f sftpFS) Abs(p string) (string, error) {
	if path.IsAbs(p) {
		return path.Clean(p), nil
	}

	// Canonicalize the base directory, not the full path: resolving the
	// whole path would follow a symlink at its final component, which
	// lstat-based transfer semantics must still see as a link.
	base, err := f.client.RealPath(".")
	if err != nil {
		return "", err
	}

	return path.Join(base, p), nil
}

// Join joins path elements with the POSIX separator.
func (f sftpFS) Join(elem ...string) string {
	return path.Join(elem...)
}

// Dir returns all but the last element of p.
func (f sftpFS) Dir(p string) string {
	return path.Dir(p)
}

// Base returns the last element of p.
func (f sftpFS) Base(p string) string {
	return path.Base(p)
}

// Contains reports whether p is root itself or lies under it.
func (f sftpFS) Contains(root, p string) bool {
	return transfer.ContainsPath(root, p, "/")
}

// CopyConcurrency prefers overlapping per-file round-trip chains: every
// file in a tree pays a serialized run of metadata round trips — open,
// chmod, write, fsync, close, rename — and between two files the server
// otherwise sits idle for the latency of each. Eight keeps it busy
// without deep queues, because the other resources are already spoken
// for: OpenSSH's sftp-server executes requests serially in one process,
// and every file shares the one SSH channel's flow-control window, so
// wider pools stop paying once the idle gaps are gone.
func (f sftpFS) CopyConcurrency() int {
	return sftpCopyConcurrency
}

// Lstat stats p without following a trailing symlink.
func (f sftpFS) Lstat(p string) (fs.FileInfo, error) {
	return f.client.Lstat(p)
}

// Stat stats p, following symlinks.
func (f sftpFS) Stat(p string) (fs.FileInfo, error) {
	return f.client.Stat(p)
}

// ReadDir lists a remote directory with lstat-style entry info.
func (f sftpFS) ReadDir(p string) ([]fs.FileInfo, error) {
	return f.client.ReadDir(p)
}

// Mkdir creates one remote directory; the engine chmods walked
// directories to their source's real mode afterward.
func (f sftpFS) Mkdir(p string) error {
	return f.client.Mkdir(p)
}

// MkdirAll creates missing remote parent directories.
func (f sftpFS) MkdirAll(p string) error {
	return f.client.MkdirAll(p)
}

// Chmod sets a remote path's permission bits.
func (f sftpFS) Chmod(p string, mode fs.FileMode) error {
	return f.client.Chmod(p, mode)
}

// SameFile cannot be answered over SFTP — the protocol exposes no file
// identity — so it conservatively reports false. The engine's same-path
// guard still applies; only aliasing through distinct names is invisible.
func (f sftpFS) SameFile(_, _ fs.FileInfo) bool {
	return false
}

// Open opens a remote file for reading.
func (f sftpFS) Open(p string) (transfer.ReadFile, error) {
	file, err := f.client.Open(p)
	if err != nil {
		return nil, err
	}

	return file, nil
}

// CreateExclusive creates a remote file for writing, failing if it
// already exists.
//
// SFTP's open carries no mode, so the server picks one — commonly 0644.
// The file is narrowed immediately: until the transfer finishes and the
// final mode is applied, the partially written content of a private file
// would otherwise be readable by every other user on the remote host.
func (f sftpFS) CreateExclusive(p string) (transfer.WriteFile, error) {
	file, err := f.client.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return nil, err
	}

	if err := file.Chmod(privateMode); err != nil {
		_ = file.Close()
		_ = f.client.Remove(p)

		return nil, err
	}

	return sftpWriteFile{File: file}, nil
}

// Rename moves oldPath over newPath atomically via the
// posix-rename@openssh.com extension; the standard SFTP rename refuses to
// replace an existing file, which would break atomic overwrite delivery.
func (f sftpFS) Rename(oldPath, newPath string) error {
	return f.client.PosixRename(oldPath, newPath)
}

// Remove deletes one remote entry.
func (f sftpFS) Remove(p string) error {
	return f.client.Remove(p)
}

// Symlink creates a remote link pointing at target.
func (f sftpFS) Symlink(target, link string) error {
	return f.client.Symlink(target, link)
}

// Readlink returns the target of a remote symlink.
func (f sftpFS) Readlink(p string) (string, error) {
	return f.client.ReadLink(p)
}

// Resolve canonicalizes p on the server, following symbolic links.
//
// SFTP's own REALPATH is not required to resolve links and some servers
// answer it purely lexically, which would silently void the transfer
// engine's containment check for followed links on a download. The
// components are walked and expanded here instead, so the guarantee holds
// against any server.
func (f sftpFS) Resolve(p string) (string, error) {
	abs, err := f.Abs(p)
	if err != nil {
		return "", err
	}

	resolved := "/"
	pending := splitPath(abs)
	hops := 0

	for len(pending) > 0 {
		name := pending[0]
		pending = pending[1:]

		switch name {
		case "", ".":
			continue
		case "..":
			resolved = path.Dir(resolved)

			continue
		}

		next := path.Join(resolved, name)

		info, err := f.client.Lstat(next)
		if err != nil {
			return "", err
		}

		if info.Mode()&fs.ModeSymlink == 0 {
			resolved = next

			continue
		}

		hops++
		if hops > maxSymlinkHops {
			return "", fmt.Errorf("resolving %q: %w", p, errTooManyLinks)
		}

		target, err := f.client.ReadLink(next)
		if err != nil {
			return "", err
		}

		if path.IsAbs(target) {
			resolved = "/"
		}

		pending = append(splitPath(target), pending...)
	}

	return resolved, nil
}

// splitPath breaks a POSIX path into its components.
func splitPath(p string) []string {
	trimmed := strings.Trim(p, "/")
	if trimmed == "" {
		return nil
	}

	return strings.Split(trimmed, "/")
}

// sftpWriteFile adapts an open remote file, tolerating servers without
// the fsync@openssh.com extension: those deliver without the flush, the
// strongest guarantee the transport offers.
type sftpWriteFile struct {
	*sftp.File
}

// Sync flushes the remote file to stable storage where the server
// supports it.
func (f sftpWriteFile) Sync() error {
	err := f.File.Sync()

	var status *sftp.StatusError
	if errors.As(err, &status) && status.FxCode() == sftp.ErrSSHFxOpUnsupported {
		return nil
	}

	return err
}
