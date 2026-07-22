package ssh

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/pkg/sftp"
	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/internal/transfer"
	"golang.org/x/crypto/ssh"
)

const (
	// privateMode is the mode a remote file is created with while it is
	// still being written, before the transfer applies its final mode.
	privateMode fs.FileMode = 0o600

	// maxSymlinkHops bounds link expansion during path resolution, so a
	// link cycle on the remote host terminates.
	maxSymlinkHops = 32
)

var (
	// errTooManyLinks reports a symlink cycle or an excessively deep chain.
	errTooManyLinks = errors.New("too many levels of symbolic links")

	// errCanceled reports setup losing a race with the transfer's own
	// cancellation; the caller reports ctx.Err() instead.
	errCanceled = errors.New("transfer canceled during setup")
)

// Upload copies a local file or directory tree to the remote host over
// SFTP, with the transfer semantics shared by every provider: atomic
// temp-and-rename delivery, mode preservation, and the configured symlink
// and special-file policy.
func (e *Environment) Upload(ctx context.Context, localPath, remotePath string, opts ...invoke.TransferOption) error {
	return e.transfer(ctx, "upload", func(remote transfer.FS) error {
		return transfer.Copy(ctx, transfer.HostFS{}, localPath, remote, remotePath, invoke.NewTransferConfig(opts...))
	})
}

// Download copies a remote file or directory tree to the local filesystem
// over SFTP, with the same semantics as Upload.
func (e *Environment) Download(ctx context.Context, remotePath, localPath string, opts ...invoke.TransferOption) error {
	return e.transfer(ctx, "download", func(remote transfer.FS) error {
		return transfer.Copy(ctx, remote, remotePath, transfer.HostFS{}, localPath, invoke.NewTransferConfig(opts...))
	})
}

// transfer opens a per-call SFTP session, runs the copy through it, and
// folds the outcome into the package taxonomy.
//
// Setup and copy both run on their own goroutine so cancellation is
// honored throughout: pkg/sftp offers no per-operation context and its
// version handshake is itself a blocking round trip, so a stalled
// connection would otherwise pin the call until TCP gave up. Tearing the
// session down from here is what unblocks it.
func (e *Environment) transfer(ctx context.Context, op string, run func(remote transfer.FS) error) error {
	if err := e.checkOpen(op); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("ssh: %s: %w", op, err)
	}

	var (
		session sftpSession
		done    = make(chan transferResult, 1)
	)

	defer session.close()

	go func() { done <- e.runTransfer(&session, op, run) }()

	select {
	case result := <-done:
		if result.err == nil {
			return nil
		}

		if result.classified {
			return result.err
		}

		return classifyTransfer(op, result.err)

	case <-ctx.Done():
		// Tear the session down to fail whatever round trip is in
		// flight, then wait for the copy to unwind so its own cleanup
		// completes before returning.
		session.close()
		<-done

		return fmt.Errorf("ssh: %s: %w", op, ctx.Err())
	}
}

// transferResult carries a transfer's outcome, distinguishing setup
// failures — already in the taxonomy — from copy failures that still need
// classifying.
type transferResult struct {
	err        error
	classified bool
}

// runTransfer starts the SFTP subsystem and runs the copy over it.
//
// The session is managed here rather than by sftp.NewClient, which leaks
// the channel when the subsystem request fails — enough repeated failures
// exhaust the server's per-connection session limit and take the whole
// Environment's command execution down with it.
func (e *Environment) runTransfer(owner *sftpSession, op string, run func(remote transfer.FS) error) transferResult {
	setupFailed := func(err error) transferResult {
		return transferResult{err: err, classified: true}
	}

	session, err := e.client.NewSession()
	if err != nil {
		return setupFailed(&invoke.TransportError{Op: op, Err: err})
	}

	if !owner.adoptSession(session) {
		_ = session.Close()

		return setupFailed(&invoke.TransportError{Op: op, Err: errCanceled})
	}

	// Discarded explicitly: an unread stderr can stall the channel.
	session.Stderr = io.Discard

	stdin, err := session.StdinPipe()
	if err != nil {
		return setupFailed(&invoke.TransportError{Op: op, Err: err})
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		return setupFailed(&invoke.TransportError{Op: op, Err: err})
	}

	// A refused subsystem is a permanent property of the server, not a
	// transient fault: classify it terminal so retry policies do not
	// hammer a host that will never serve SFTP.
	if err := session.RequestSubsystem("sftp"); err != nil {
		return setupFailed(fmt.Errorf("ssh: %s: sftp subsystem: %w", op, invoke.ErrNotSupported))
	}

	client, err := sftp.NewClientPipe(stdout, stdin)
	if err != nil {
		return setupFailed(&invoke.TransportError{Op: op, Err: err})
	}

	if !owner.adoptClient(client) {
		_ = client.Close()

		return setupFailed(&invoke.TransportError{Op: op, Err: errCanceled})
	}

	return transferResult{err: run(sftpFS{client: client})}
}

// sftpSession owns one transfer's session and client so cancellation can
// tear them down from another goroutine at any point during setup or the
// copy, and so teardown happens exactly once.
type sftpSession struct {
	mu      sync.Mutex
	closed  bool
	session *ssh.Session
	client  *sftp.Client
}

// adoptSession takes ownership of session, reporting false if the
// transfer was already torn down.
func (s *sftpSession) adoptSession(session *ssh.Session) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return false
	}

	s.session = session

	return true
}

// adoptClient takes ownership of client, reporting false if the transfer
// was already torn down.
func (s *sftpSession) adoptClient(client *sftp.Client) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return false
	}

	s.client = client

	return true
}

// close tears the transfer's session down. It is idempotent and safe to
// call while setup is still in flight.
func (s *sftpSession) close() {
	s.mu.Lock()

	if s.closed {
		s.mu.Unlock()

		return
	}

	s.closed = true
	client, session := s.client, s.session
	s.mu.Unlock()

	if client != nil {
		_ = client.Close()
	}

	if session != nil {
		_ = session.Close()
	}
}

// classifyTransfer wraps a transfer failure, promoting a lost connection
// to TransportError so the executor's retry policy can act on it; the
// atomic delivery makes a retried transfer safe.
func classifyTransfer(op string, err error) error {
	if isTransportFailure(err) {
		return &invoke.TransportError{Op: op, Err: err}
	}

	return fmt.Errorf("ssh: %s: %w", op, err)
}

// isTransportFailure reports whether err is the connection dying rather
// than the operation being refused.
//
// One outage surfaces two ways: the receive loop broadcasts the SFTP
// connection-lost status, or a packet send loses the race to it and
// reports the raw write failure instead. Matching only the status would
// make retryability a coin flip on the same disconnect.
func isTransportFailure(err error) bool {
	if errors.Is(err, sftp.ErrSSHFxConnectionLost) || errors.Is(err, sftp.ErrSSHFxNoConnection) {
		return true
	}

	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return true
	}

	var opErr *net.OpError

	return errors.As(err, &opErr)
}

// sftpFS adapts an SFTP session to the transfer engine's FS. Remote paths
// are POSIX regardless of the host platform, so the path package — not
// filepath — does the path algebra.
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
	return p == root || strings.HasPrefix(p, root+"/")
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

// randomSuffix returns hex material for a name no concurrent command will
// collide with.
//
// A failure to gather randomness is reported rather than papered over with
// a fixed string: a predictable name is one an attacker can pre-create, so
// the caller must fail closed instead of writing to it.
func randomSuffix() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("ssh: generating a unique name: %w", err)
	}

	return hex.EncodeToString(buf[:]), nil
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
