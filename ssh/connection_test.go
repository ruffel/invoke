package ssh_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ruffel/invoke/ssh"
	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// writeKnownHosts records key for the server's address in a known_hosts
// file and returns its path.
func writeKnownHosts(t *testing.T, srv *testServer, key xssh.PublicKey) string {
	t.Helper()

	addr := net.JoinHostPort(srv.host(), strconv.Itoa(srv.port()))
	line := knownhosts.Line([]string{knownhosts.Normalize(addr)}, key)

	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("writing known_hosts: %v", err)
	}

	return path
}

// newECDSAHostKey returns a host key of the type an unconstrained client
// prefers over Ed25519.
func newECDSAHostKey(t *testing.T) xssh.Signer {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa key: %v", err)
	}

	signer, err := xssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("ecdsa signer: %v", err)
	}

	return signer
}

// TestKnownHostsConstrainsHostKeyAlgorithms checks a host recorded under
// one key type still verifies when the server also offers a type the
// client prefers. Without constraining negotiation to what known_hosts
// records, the server answers with its preferred key and the connection
// fails as if the host were unknown — the warning that means a
// machine-in-the-middle.
func TestKnownHostsConstrainsHostKeyAlgorithms(t *testing.T) {
	t.Parallel()

	srv := startTestServer(t, withExtraHostKey(newECDSAHostKey(t)))

	// known_hosts records only the Ed25519 key. The server also offers an
	// ECDSA key, which an unconstrained client prefers — so it is the one
	// the server would answer with, and it is not in known_hosts.
	path := writeKnownHosts(t, srv, srv.hostKey)

	env, err := ssh.New(srv.host(),
		ssh.WithPort(srv.port()),
		ssh.WithUser("tester"),
		ssh.WithPassword(testPassword),
		ssh.WithKnownHosts(path),
	)
	if err != nil {
		t.Fatalf("New with a host recorded under a non-preferred key type = %v", err)
	}

	t.Cleanup(func() { _ = env.Close() })

	if _, err := env.LookPath(t.Context(), "sh"); err != nil {
		t.Errorf("LookPath = %v", err)
	}
}

// TestKnownHostsRejectsUnrecordedHost checks the known_hosts path still
// fails closed: constraining algorithms must not turn into trusting
// whatever the server presents.
func TestKnownHostsRejectsUnrecordedHost(t *testing.T) {
	t.Parallel()

	srv := startTestServer(t)
	other := startTestServer(t)

	// Record a different server's key for this address.
	path := writeKnownHosts(t, srv, other.hostKey)

	_, err := ssh.New(srv.host(),
		ssh.WithPort(srv.port()),
		ssh.WithUser("tester"),
		ssh.WithPassword(testPassword),
		ssh.WithKnownHosts(path),
	)
	if err == nil {
		t.Fatal("New accepted a host whose key does not match known_hosts")
	}
}

// TestKeepAliveProbesTheServer checks the connection is probed, so a link
// that dies silently is discovered rather than leaving the next operation
// waiting on a socket nobody serves.
func TestKeepAliveProbesTheServer(t *testing.T) {
	t.Parallel()

	srv := startTestServer(t)

	env, err := ssh.New(srv.host(),
		ssh.WithPort(srv.port()),
		ssh.WithUser("tester"),
		ssh.WithPassword(testPassword),
		ssh.WithHostKeyCallback(xssh.FixedHostKey(srv.hostKey)),
		ssh.WithKeepAlive(20*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("ssh.New = %v", err)
	}

	t.Cleanup(func() { _ = env.Close() })

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if srv.keepAliveCount() > 0 {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Errorf("no keepalive probe reached the server in 5s")
}

// TestKeepAliveStopsOnClose checks the probe loop does not outlive the
// connection it was watching.
func TestKeepAliveStopsOnClose(t *testing.T) {
	t.Parallel()

	srv := startTestServer(t)

	env, err := ssh.New(srv.host(),
		ssh.WithPort(srv.port()),
		ssh.WithUser("tester"),
		ssh.WithPassword(testPassword),
		ssh.WithHostKeyCallback(xssh.FixedHostKey(srv.hostKey)),
		ssh.WithKeepAlive(20*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("ssh.New = %v", err)
	}

	// Let at least one probe land, then close and confirm it stops.
	time.Sleep(100 * time.Millisecond)

	if err := env.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}

	settled := srv.keepAliveCount()

	time.Sleep(200 * time.Millisecond)

	if got := srv.keepAliveCount(); got != settled {
		t.Errorf("keepalive probes continued after Close: %d then %d", settled, got)
	}
}

// TestAgentSocketIsReleasedOnClose checks the SSH agent connection, held
// open for the life of the connection because agent authentication signs
// on demand, is released when the environment closes.
func TestAgentSocketIsReleasedOnClose(t *testing.T) {
	// A unix socket path is limited to about 100 bytes, which the usual
	// per-test temp directory can exceed.
	//nolint:usetesting // t.TempDir can exceed the unix socket path limit.
	dir, err := os.MkdirTemp("", "iv")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	agentPath := filepath.Join(dir, "a.sock")

	listener, err := net.Listen("unix", agentPath) //nolint:noctx // Local test socket; no context to bound.
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}

	t.Cleanup(func() { _ = listener.Close() })

	closed := make(chan struct{}, 1)

	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}

		// The agent never answers; the client falls back to password
		// auth. Reading returns once the client closes the socket.
		buf := make([]byte, 1)
		_, _ = conn.Read(buf)
		_ = conn.Close()

		closed <- struct{}{}
	}()

	t.Setenv("SSH_AUTH_SOCK", agentPath)

	srv := startTestServer(t)

	env, err := ssh.New(srv.host(),
		ssh.WithPort(srv.port()),
		ssh.WithUser("tester"),
		ssh.WithPassword(testPassword),
		ssh.WithAgent(),
		ssh.WithHostKeyCallback(xssh.FixedHostKey(srv.hostKey)),
	)
	if err != nil {
		t.Fatalf("ssh.New = %v", err)
	}

	if err := env.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}

	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Error("the agent socket was still open after Close; it leaks for the process lifetime")
	}
}
