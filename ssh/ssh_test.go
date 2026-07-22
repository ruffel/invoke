package ssh_test

import (
	"context"
	"testing"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/invoketest"
	"github.com/ruffel/invoke/ssh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	xssh "golang.org/x/crypto/ssh"
)

// dialTestServer connects a provider Environment to a fresh in-process
// server, verifying its host key and authenticating by password.
func dialTestServer(t *testing.T) *ssh.Environment {
	t.Helper()

	srv := startTestServer(t)

	env, err := ssh.New(t.Context(), srv.host(),
		ssh.WithPort(srv.port()),
		ssh.WithUser("tester"),
		ssh.WithPassword(testPassword),
		ssh.WithHostKeyCallback(xssh.FixedHostKey(srv.hostKey)),
	)
	require.NoError(t, err)

	t.Cleanup(func() { _ = env.Close() })

	return env
}

// asTestingT recovers the *testing.T the contract runs under. Verify passes
// each contract its subtest's own *testing.T, whose Cleanup fires when the
// contract ends — exactly the scope for tearing down a per-contract server.
func asTestingT(it invoketest.T) *testing.T {
	tt, ok := it.(*testing.T)
	if !ok {
		panic("ssh contract tests require the standard *testing.T")
	}

	return tt
}

// TestSSHContractSuite runs the shared behavioral contracts against the SSH
// provider talking to the embedded server.
func TestSSHContractSuite(t *testing.T) {
	t.Parallel()

	invoketest.Verify(t, func(it invoketest.T) invoke.Environment {
		return dialTestServer(asTestingT(it))
	}, invoketest.WithKnownGap("lifecycle/cancel-during-drain-keeps-outcome",
		"the outcome is read from the context before the session's own exit status, so a cancellation "+
			"arriving while output drains rewrites a completed exit; fixed separately"))
}

func TestConnectionRejectsWrongPassword(t *testing.T) {
	t.Parallel()

	srv := startTestServer(t)

	_, err := ssh.New(t.Context(), srv.host(),
		ssh.WithPort(srv.port()),
		ssh.WithPassword("wrong"),
		ssh.WithHostKeyCallback(xssh.FixedHostKey(srv.hostKey)),
	)
	require.Error(t, err, "New with a wrong password succeeded, want an auth failure")
}

func TestConnectionRequiresHostKeyVerification(t *testing.T) {
	t.Parallel()

	srv := startTestServer(t)

	// No known_hosts, no callback, no insecure override: fail closed.
	_, err := ssh.New(t.Context(), srv.host(),
		ssh.WithPort(srv.port()),
		ssh.WithPassword(testPassword),
	)
	require.Error(t, err, "New without host-key verification succeeded; it must fail closed")
}

func TestWrongHostKeyIsRejected(t *testing.T) {
	t.Parallel()

	srv := startTestServer(t)
	other := startTestServer(t)

	// Verify against a different server's key: the handshake must fail.
	_, err := ssh.New(t.Context(), srv.host(),
		ssh.WithPort(srv.port()),
		ssh.WithPassword(testPassword),
		ssh.WithHostKeyCallback(xssh.FixedHostKey(other.hostKey)),
	)
	require.Error(t, err, "New accepted a mismatched host key")
}

func TestClosedEnvironmentRefusesEverything(t *testing.T) {
	t.Parallel()

	env := dialTestServer(t)
	_ = env.Close()

	ctx := context.Background()

	_, err := env.Start(ctx, invoke.New("true"), invoke.IO{})
	assert.ErrorIs(t, err, invoke.ErrClosed, "Start after Close")

	_, err = env.LookPath(ctx, "sh")
	assert.ErrorIs(t, err, invoke.ErrClosed, "LookPath after Close")
}
