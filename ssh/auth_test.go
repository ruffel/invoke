package ssh_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/ssh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// newClientKey generates a fresh client key pair: the private half for
// the client side, the public half for the server to authorize.
func newClientKey(t *testing.T) (ed25519.PrivateKey, xssh.PublicKey) {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err, "client key")

	signer, err := xssh.NewSignerFromKey(priv)
	require.NoError(t, err, "client signer")

	return priv, signer.PublicKey()
}

// writeKeyFile writes priv in OpenSSH PEM form, encrypted when a
// passphrase is given, and returns its path.
func writeKeyFile(t *testing.T, priv ed25519.PrivateKey, passphrase string) string {
	t.Helper()

	var (
		block *pem.Block
		err   error
	)

	if passphrase == "" {
		block, err = xssh.MarshalPrivateKey(priv, "invoke test key")
	} else {
		block, err = xssh.MarshalPrivateKeyWithPassphrase(priv, "invoke test key", []byte(passphrase))
	}

	require.NoError(t, err, "marshal key")

	path := filepath.Join(t.TempDir(), "id_ed25519")
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(block), 0o600), "write key")

	return path
}

// dialWithAuth connects to the server with the given authentication
// options and nothing else — no password to fall back on, so the method
// under test is the one that must have worked.
func dialWithAuth(t *testing.T, srv *testServer, opts ...ssh.Option) *ssh.Environment {
	t.Helper()

	base := []ssh.Option{
		ssh.WithPort(srv.port()),
		ssh.WithUser("tester"),
		ssh.WithHostKeyCallback(xssh.FixedHostKey(srv.hostKey)),
	}

	env, err := ssh.New(t.Context(), srv.host(), append(base, opts...)...)
	require.NoError(t, err)

	t.Cleanup(func() { _ = env.Close() })

	return env
}

// runEcho proves a connection is genuinely usable, not merely opened.
func runEcho(t *testing.T, env *ssh.Environment) {
	t.Helper()

	out, result, err := runOutput(t, env, invoke.New("echo", "authenticated"))
	require.NoError(t, err)
	require.Equal(t, 0, result.ExitCode)
	assert.Equal(t, "authenticated", strings.TrimSpace(out))
}

// TestPrivateKeyAuthentication covers the dominant real-world way in: a
// key file on disk, plain or passphrase-protected, with no password
// configured at all.
func TestPrivateKeyAuthentication(t *testing.T) {
	t.Parallel()

	t.Run("plain key file", func(t *testing.T) {
		t.Parallel()

		priv, pub := newClientKey(t)
		srv := startTestServer(t, withAuthorizedKey(pub))

		env := dialWithAuth(t, srv, ssh.WithPrivateKey(writeKeyFile(t, priv, "")))
		runEcho(t, env)
	})

	t.Run("passphrase-protected key file", func(t *testing.T) {
		t.Parallel()

		const passphrase = "open sesame"

		priv, pub := newClientKey(t)
		srv := startTestServer(t, withAuthorizedKey(pub))

		env := dialWithAuth(t, srv,
			ssh.WithPrivateKey(writeKeyFile(t, priv, passphrase)),
			ssh.WithPrivateKeyPassphrase(passphrase))
		runEcho(t, env)
	})
}

// TestPrivateKeyProblemsAreNamed pins what a caller hears when their
// only configured method cannot be assembled: the reason, by name,
// before anything is dialed.
func TestPrivateKeyProblemsAreNamed(t *testing.T) {
	t.Parallel()

	t.Run("missing key file", func(t *testing.T) {
		t.Parallel()

		_, err := ssh.New(t.Context(), "127.0.0.1",
			ssh.WithUser("tester"),
			ssh.WithPrivateKey(filepath.Join(t.TempDir(), "absent")),
			ssh.WithInsecureIgnoreHostKey())
		require.Error(t, err, "an unreadable only-method cannot connect")

		assert.Contains(t, err.Error(), "no usable authentication method")
		assert.Contains(t, err.Error(), "reading private key")
	})

	t.Run("wrong passphrase", func(t *testing.T) {
		t.Parallel()

		priv, _ := newClientKey(t)
		path := writeKeyFile(t, priv, "right")

		_, err := ssh.New(t.Context(), "127.0.0.1",
			ssh.WithUser("tester"),
			ssh.WithPrivateKey(path),
			ssh.WithPrivateKeyPassphrase("wrong"),
			ssh.WithInsecureIgnoreHostKey())
		require.Error(t, err, "an undecryptable only-method cannot connect")

		assert.Contains(t, err.Error(), "no usable authentication method")
		assert.Contains(t, err.Error(), "parsing private key")
	})
}

// TestAgentAuthentication runs a real signing round-trip: a keyring
// agent served over a unix socket, the provider connecting through
// SSH_AUTH_SOCK, and the server accepting the agent-held key. It stays
// serial: t.Setenv, which SSH_AUTH_SOCK needs, forbids t.Parallel.
func TestAgentAuthentication(t *testing.T) {
	priv, pub := newClientKey(t)
	srv := startTestServer(t, withAuthorizedKey(pub))

	keyring := agent.NewKeyring()
	require.NoError(t, keyring.Add(agent.AddedKey{PrivateKey: priv}), "seed agent")

	// A dedicated short temp dir: unix socket paths have a low length
	// limit, and t.TempDir carries the whole test name.
	dir, err := os.MkdirTemp("", "invoke-agent") //nolint:usetesting // t.TempDir embeds the test name; socket paths have a low length limit.
	require.NoError(t, err)

	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	socket := filepath.Join(dir, "agent.sock")

	listener, err := net.Listen("unix", socket) //nolint:noctx // Test listener.
	require.NoError(t, err, "agent socket")

	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			go func() { _ = agent.ServeAgent(keyring, conn) }()
		}
	}()

	t.Setenv("SSH_AUTH_SOCK", socket)

	env := dialWithAuth(t, srv, ssh.WithAgent())
	runEcho(t, env)
}
