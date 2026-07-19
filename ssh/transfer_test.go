package ssh_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/ssh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	xssh "golang.org/x/crypto/ssh"
)

// dialServer connects a provider Environment to an already-started
// server, verifying its host key and authenticating by password.
func dialServer(t *testing.T, srv *testServer) *ssh.Environment {
	t.Helper()

	env, err := ssh.New(srv.host(),
		ssh.WithPort(srv.port()),
		ssh.WithUser("tester"),
		ssh.WithPassword(testPassword),
		ssh.WithHostKeyCallback(xssh.FixedHostKey(srv.hostKey)),
	)
	require.NoError(t, err)

	t.Cleanup(func() { _ = env.Close() })

	return env
}

// TestUploadTempFileIsPrivateWhileWriting checks the in-flight temporary
// file is not readable by other users on the remote host. SFTP's open
// carries no mode, so an unnarrowed temp would expose the content of a
// private file for the whole transfer.
func TestUploadTempFileIsPrivateWhileWriting(t *testing.T) {
	t.Parallel()

	env := dialServer(t, startTestServer(t))

	src := filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(src, []byte(strings.Repeat("s", 1<<20)), 0o600), "writing fixture")

	dstDir := t.TempDir()
	dst := filepath.Join(dstDir, "secret.txt")

	var (
		observed  fs.FileMode
		sawTemp   bool
		checkOnce bool
	)

	// The test server serves the host filesystem, so the destination
	// directory can be inspected directly while the bytes are moving.
	err := env.Upload(t.Context(), src, dst, invoke.WithProgress(func(_ invoke.TransferProgress) {
		if checkOnce {
			return
		}

		entries, readErr := os.ReadDir(dstDir)
		if readErr != nil {
			return
		}

		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), ".invoke-") {
				continue
			}

			info, infoErr := entry.Info()
			if infoErr != nil {
				continue
			}

			observed = info.Mode().Perm()
			sawTemp = true
			checkOnce = true
		}
	}))
	require.NoError(t, err)

	if !sawTemp {
		t.Skip("transfer completed before the temporary file could be observed")
	}

	assert.Zero(t, observed&0o077, "in-flight temporary file mode = %v, want no group or world access", observed)
}

// TestDownloadFollowRejectsEscapes checks the symlink containment
// guarantee holds on downloads. The engine relies on the remote side
// canonicalizing paths; a server answering REALPATH lexically would let a
// followed link read outside the transfer root.
func TestDownloadFollowRejectsEscapes(t *testing.T) {
	t.Parallel()

	env := dialServer(t, startTestServer(t))

	outsideDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outsideDir, "secret.txt"), []byte("outside data"), 0o600),
		"writing fixture")

	remoteDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(remoteDir, "real.txt"), []byte("inside"), 0o600), "writing fixture")

	escape := filepath.Join(remoteDir, "escape.txt")
	require.NoError(t, os.Symlink(filepath.Join(outsideDir, "secret.txt"), escape), "symlink")

	local := filepath.Join(t.TempDir(), "downloaded")

	err := env.Download(t.Context(), remoteDir, local, invoke.WithSymlinks(invoke.SymlinkFollow))
	require.Error(t, err, "Download followed a link out of the transfer root; it must be rejected")

	assert.ErrorContains(t, err, "escape.txt", "the error does not name the offending link")

	got, readErr := os.ReadFile(filepath.Join(local, "escape.txt"))
	assert.Error(t, readErr, "outside content leaked into the destination: %q", got)
}

// TestDownloadFollowsInRootSymlinks checks that resolving links by hand
// did not break the legitimate case: an in-root link still follows to its
// target's content.
func TestDownloadFollowsInRootSymlinks(t *testing.T) {
	t.Parallel()

	env := dialServer(t, startTestServer(t))

	remoteDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(remoteDir, "real.txt"), []byte("followed content"), 0o600),
		"writing fixture")

	require.NoError(t, os.Symlink("real.txt", filepath.Join(remoteDir, "link.txt")), "symlink")

	local := filepath.Join(t.TempDir(), "downloaded")

	require.NoError(t, env.Download(t.Context(), remoteDir, local, invoke.WithSymlinks(invoke.SymlinkFollow)))

	got, err := os.ReadFile(filepath.Join(local, "link.txt"))
	require.NoError(t, err, "reading followed link")

	assert.Equal(t, "followed content", string(got))
}

// TestTransferWithoutSFTPSubsystemIsTerminal checks a host that does not
// serve SFTP is reported as an unsupported operation rather than a
// transport fault, and that the refused sessions do not leak — enough
// leaked channels would exhaust the connection's session limit and take
// command execution down with it.
func TestTransferWithoutSFTPSubsystemIsTerminal(t *testing.T) {
	t.Parallel()

	srv := startTestServer(t, withoutSFTP())
	env := dialServer(t, srv)

	src := filepath.Join(t.TempDir(), "file.txt")
	require.NoError(t, os.WriteFile(src, []byte("payload"), 0o600), "writing fixture")

	dst := filepath.Join(t.TempDir(), "file.txt")

	const attempts = 12

	for range attempts {
		err := env.Upload(t.Context(), src, dst)
		require.ErrorIs(t, err, invoke.ErrNotSupported, "Upload against a host without SFTP")

		var transportErr *invoke.TransportError

		require.NotErrorAs(t, err, &transportErr, "Upload must be a terminal error, not a retryable TransportError")
	}

	waitForSessions(t, srv, 0)

	// The connection must still be usable for commands.
	_, err := env.LookPath(t.Context(), "sh")
	assert.NoError(t, err, "LookPath after %d refused transfers; the connection was exhausted", attempts)
}

// TestTransferCancelsWhileServerStalls checks cancellation is honored
// while an SFTP round trip is blocked. Without tearing the session down,
// a half-open connection would pin the call until TCP gave up.
func TestTransferCancelsWhileServerStalls(t *testing.T) {
	t.Parallel()

	srv := startTestServer(t, withStalledSFTP())
	env := dialServer(t, srv)

	src := filepath.Join(t.TempDir(), "file.txt")
	require.NoError(t, os.WriteFile(src, []byte("payload"), 0o600), "writing fixture")

	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)

	go func() { done <- env.Upload(ctx, src, filepath.Join(t.TempDir(), "file.txt")) }()

	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.DeadlineExceeded, "stalled Upload")
	case <-time.After(10 * time.Second):
		require.Fail(t,
			"Upload did not return after its context expired; cancellation is not honored while a round trip blocks")
	}

	waitForSessions(t, srv, 0)
}

// waitForSessions waits for the server's open session count to settle at
// want, so a teardown that is merely asynchronous is not read as a leak.
func waitForSessions(t *testing.T, srv *testServer, want int) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if srv.openSessions() == want {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	assert.Failf(t, "sessions leaked", "open sessions = %d, want %d", srv.openSessions(), want)
}
