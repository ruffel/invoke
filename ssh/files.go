package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/internal/transfer"
	"golang.org/x/crypto/ssh"
)

// errCanceled reports setup losing a race with the transfer's own
// cancellation; the caller reports ctx.Err() instead.
var errCanceled = errors.New("transfer canceled during setup")

// sftpIdleGrace is how long a canceled transfer's session may go without
// a byte from the server before it is torn down under the copy that is
// unwinding over it.
//
// It bounds silence rather than the unwind itself. How long cleanup takes
// is a property of the link and of how many files were in flight; how
// long a server may say nothing at all is not, so a slow link gets the
// round trips it needs and only one that has stopped answering is cut
// off.
const sftpIdleGrace = 2 * time.Second

// idleChecks is how many times the unwind samples the session's silence
// before the grace is up, trading a little polling for not overshooting
// the bound by a whole grace.
const idleChecks = 4

// Upload copies a local file or directory tree to the remote host over
// SFTP, with the transfer semantics shared by every provider: atomic
// temp-and-rename delivery, mode preservation, and the configured symlink
// and special-file policy.
func (e *Environment) Upload(ctx context.Context, localPath, remotePath string, opts ...invoke.TransferOption) error {
	return e.transfer(ctx, "upload", func(ctx context.Context, remote transfer.FS) error {
		return transfer.Copy(ctx, transfer.HostFS{}, localPath, remote, remotePath, invoke.NewTransferConfig(opts...))
	})
}

// Download copies a remote file or directory tree to the local filesystem
// over SFTP, with the same semantics as Upload.
func (e *Environment) Download(ctx context.Context, remotePath, localPath string, opts ...invoke.TransferOption) error {
	return e.transfer(ctx, "download", func(ctx context.Context, remote transfer.FS) error {
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
// session down from here is what unblocks it, once the copy has had the
// chance to unwind over it.
//
// The transfer runs for the life of the Environment: Close cancels it and
// waits for it to unwind before the connection goes, so a stopped copy
// still removes its temp file, and it then fails wrapping ErrClosed. The
// connection failing under it is a transport fault; Close closing it is
// not, and would otherwise read as one.
func (e *Environment) transfer(ctx context.Context, op string, run func(context.Context, transfer.FS) error) error {
	err := e.life.Run(ctx, func(ctx context.Context) error {
		// Asked before a session is opened for a transfer that will not
		// run.
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("ssh: %s: %w", op, err)
		}

		return e.runSession(ctx, op, run)
	})

	// Refused, or stopped, by Close: the one outcome nothing above got
	// to name.
	if errors.Is(err, invoke.ErrClosed) {
		return fmt.Errorf("ssh: %s: %w", op, err)
	}

	return err
}

// runSession runs one transfer over its own SFTP session.
func (e *Environment) runSession(ctx context.Context, op string, run func(context.Context, transfer.FS) error) error {
	var (
		session sftpSession
		done    = make(chan transferResult, 1)
	)

	defer session.close()

	go func() { done <- e.runTransfer(ctx, &session, op, run) }()

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
		session.unwind(done, sftpIdleGrace)

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
func (e *Environment) runTransfer(
	ctx context.Context,
	owner *sftpSession,
	op string,
	run func(context.Context, transfer.FS) error,
) transferResult {
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

	// Concurrent writes pipeline upload requests instead of paying a
	// round trip per 32 KiB packet. The option's documented hazard — a
	// file left with holes when a transfer dies mid-flight — never
	// reaches a destination path here: the engine writes into an
	// exclusively created temp file and renames over the destination
	// only on success, so a torn file is discarded, not delivered.
	client, err := sftp.NewClientPipe(owner.watch(stdout), stdin, sftp.UseConcurrentWrites(true))
	if err != nil {
		return setupFailed(&invoke.TransportError{Op: op, Err: err})
	}

	if !owner.adoptClient(client) {
		_ = client.Close()

		return setupFailed(&invoke.TransportError{Op: op, Err: errCanceled})
	}

	return transferResult{err: run(ctx, sftpFS{client: client})}
}

// sftpSession owns one transfer's session and client so cancellation can
// tear them down from another goroutine at any point during setup or the
// copy, and so teardown happens exactly once.
type sftpSession struct {
	mu      sync.Mutex
	closed  bool
	session *ssh.Session
	client  *sftp.Client

	// lastAnswer is when the server last sent a byte, in Unix
	// nanoseconds. A canceled transfer reads it to tell a session that
	// is still working from one that has stopped answering.
	lastAnswer atomic.Int64
}

// watch wraps the SFTP transport's read side so every answer from the
// server is timestamped. Installed before the client is built, so the
// version handshake counts as an answer like any other.
func (s *sftpSession) watch(r io.Reader) io.Reader {
	s.touch()

	return &activityReader{inner: r, session: s}
}

// touch records that the server has just answered.
func (s *sftpSession) touch() {
	s.lastAnswer.Store(time.Now().UnixNano())
}

// idleFor reports how long the server has said nothing.
func (s *sftpSession) idleFor() time.Duration {
	return time.Since(time.Unix(0, s.lastAnswer.Load()))
}

// started reports whether the copy has a client to unwind over.
func (s *sftpSession) started() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.client != nil
}

// unwind waits for a canceled copy to finish and tears the session down
// if it cannot.
//
// A copy that notices the cancellation removes the temp file it was
// writing on the way out, and needs this session to do it: closing the
// session first is what left those files on the target. So the copy is
// waited for, and the session is pulled from under it only once the
// server has been silent for idle, which is the one state waiting cannot
// resolve.
func (s *sftpSession) unwind(done <-chan transferResult, idle time.Duration) {
	// Before the client exists there is no copy holding the session and
	// no temp file to remove: the round trip in flight is the subsystem
	// handshake, which only teardown can end.
	if s.started() && !wentSilent(done, s.idleFor, idle) {
		return
	}

	s.close()
	<-done
}

// wentSilent waits for the copy to finish, reporting true instead if the
// session first goes idle for the given duration. idleFor is passed in so
// the decision can be tested without a live session.
func wentSilent(done <-chan transferResult, idleFor func() time.Duration, idle time.Duration) bool {
	ticker := time.NewTicker(max(idle/idleChecks, time.Millisecond))
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return false

		case <-ticker.C:
			if idleFor() >= idle {
				return true
			}
		}
	}
}

// activityReader timestamps the session on every answer that arrives.
type activityReader struct {
	inner   io.Reader
	session *sftpSession
}

func (r *activityReader) Read(p []byte) (int, error) {
	n, err := r.inner.Read(p)
	if n > 0 {
		r.session.touch()
	}

	return n, err
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

	// The session goes first. The client's Close waits for its receive
	// loop, which is reading from the server and ends only when the
	// server answers or the channel closes: on a server that has stopped
	// answering, closing the client first waits forever.
	if session != nil {
		_ = session.Close()
	}

	if client != nil {
		_ = client.Close()
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
