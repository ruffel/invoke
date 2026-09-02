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

// startAgent serves a keyring holding the given keys over a unix socket
// and returns the socket path for SSH_AUTH_SOCK. Pointing that variable
// at it takes t.Setenv, which forbids t.Parallel, so every test using it
// stays serial.
func startAgent(t *testing.T, keys ...ed25519.PrivateKey) string {
	t.Helper()

	keyring := agent.NewKeyring()
	for _, key := range keys {
		require.NoError(t, keyring.Add(agent.AddedKey{PrivateKey: key}), "seed agent")
	}

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

	return socket
}

// TestAgentAuthentication runs a real signing round-trip: a keyring
// agent served over a unix socket, the provider connecting through
// SSH_AUTH_SOCK, and the server accepting the agent-held key.
func TestAgentAuthentication(t *testing.T) {
	priv, pub := newClientKey(t)
	srv := startTestServer(t, withAuthorizedKey(pub))

	t.Setenv("SSH_AUTH_SOCK", startAgent(t, priv))

	env := dialWithAuth(t, srv, ssh.WithAgent())
	runEcho(t, env)
}

// TestAgentKeysAreOfferedAlongsideAKeyFile pins that naming a key file
// does not silence the agent. The protocol calls both "publickey", and a
// client offers each method name once, so were the two registered
// separately the agent would go unasked whenever the file key was
// refused — the everyday case of a stale key on disk and the current
// one in the agent.
func TestAgentKeysAreOfferedAlongsideAKeyFile(t *testing.T) {
	stalePriv, stalePub := newClientKey(t)
	currentPriv, currentPub := newClientKey(t)
	srv := startTestServer(t, withAuthorizedKey(currentPub))

	t.Setenv("SSH_AUTH_SOCK", startAgent(t, currentPriv))

	env := dialWithAuth(t, srv,
		ssh.WithPrivateKey(writeKeyFile(t, stalePriv, "")),
		ssh.WithAgent())
	runEcho(t, env)

	assert.Equal(t, []string{
		"publickey " + xssh.FingerprintSHA256(stalePub),
		"publickey " + xssh.FingerprintSHA256(currentPub),
	}, srv.authSeen(), "one connection offered the file key, was refused, and went on to the agent's key")
}

// TestAgentProblemsAreNamed pins what a caller hears when the agent is
// their only configured method and it cannot supply a key.
func TestAgentProblemsAreNamed(t *testing.T) {
	t.Run("SSH_AUTH_SOCK unset", func(t *testing.T) {
		t.Setenv("SSH_AUTH_SOCK", "")

		_, err := ssh.New(t.Context(), "127.0.0.1",
			ssh.WithUser("tester"),
			ssh.WithAgent(),
			ssh.WithInsecureIgnoreHostKey())
		require.Error(t, err, "an unreachable only-method cannot connect")

		assert.Contains(t, err.Error(), "no usable authentication method")
		assert.Contains(t, err.Error(), "SSH_AUTH_SOCK is unset")
	})

	t.Run("agent holds no keys", func(t *testing.T) {
		t.Setenv("SSH_AUTH_SOCK", startAgent(t))

		_, err := ssh.New(t.Context(), "127.0.0.1",
			ssh.WithUser("tester"),
			ssh.WithAgent(),
			ssh.WithInsecureIgnoreHostKey())
		require.Error(t, err, "an empty only-method cannot connect")

		assert.Contains(t, err.Error(), "no usable authentication method")
		assert.Contains(t, err.Error(), "agent holds no keys")
	})
}

// TestPublicKeysAreOfferedBeforeThePassword pins the order, which is
// OpenSSH's: a caller who sets both has the key tried first, and the
// password is sent only when no key was accepted — never to a host that
// would have taken the key.
func TestPublicKeysAreOfferedBeforeThePassword(t *testing.T) {
	t.Parallel()

	t.Run("an accepted key keeps the password off the wire", func(t *testing.T) {
		t.Parallel()

		priv, pub := newClientKey(t)
		srv := startTestServer(t, withAuthorizedKey(pub))

		// The password is given first to show that option order does not
		// decide.
		env := dialWithAuth(t, srv,
			ssh.WithPassword(testPassword),
			ssh.WithPrivateKey(writeKeyFile(t, priv, "")))
		runEcho(t, env)

		assert.Equal(t, []string{"publickey " + xssh.FingerprintSHA256(pub)}, srv.authSeen(),
			"a valid password was configured and never sent: the key logged in first")
	})

	t.Run("a refused key falls through to the password", func(t *testing.T) {
		t.Parallel()

		priv, pub := newClientKey(t)
		_, other := newClientKey(t)
		srv := startTestServer(t, withAuthorizedKey(other))

		env := dialWithAuth(t, srv,
			ssh.WithPrivateKey(writeKeyFile(t, priv, "")),
			ssh.WithPassword(testPassword))
		runEcho(t, env)

		assert.Equal(t, []string{"publickey " + xssh.FingerprintSHA256(pub), "password"}, srv.authSeen(),
			"the password is the fallback, tried only once the key was refused")
	})
}
