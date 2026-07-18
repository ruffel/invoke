package ssh_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/invoketest"
	"github.com/ruffel/invoke/ssh"
	xssh "golang.org/x/crypto/ssh"
)

// dialTestServer connects a provider Environment to a fresh in-process
// server, verifying its host key and authenticating by password.
func dialTestServer(t *testing.T) *ssh.Environment {
	t.Helper()

	srv := startTestServer(t)

	env, err := ssh.New(srv.host(),
		ssh.WithPort(srv.port()),
		ssh.WithUser("tester"),
		ssh.WithPassword(testPassword),
		ssh.WithHostKeyCallback(xssh.FixedHostKey(srv.hostKey)),
	)
	if err != nil {
		t.Fatalf("ssh.New = %v", err)
	}

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

// transferGaps declares the transfer contracts as known gaps until the
// SFTP-backed file transfer lands.
func transferGaps() []invoketest.Option {
	ids := []string{
		"transfer/roundtrip-preserves-content-and-mode",
		"transfer/binary-content-survives",
		"transfer/mode-override-applies-on-overwrite",
		"transfer/failure-preserves-destination",
		"transfer/cancel-preserves-destination",
		"transfer/download-cancel-preserves-destination",
		"transfer/tree-roundtrip-creates-parents",
		"transfer/empty-files-and-dirs",
		"transfer/symlinks-preserve",
		"transfer/symlink-follow-copies-content",
		"transfer/follow-rejects-escapes",
		"transfer/special-files-error-by-default",
		"transfer/progress-reports-totals",
		"transfer/canceled-before-start-does-nothing",
	}

	opts := make([]invoketest.Option, 0, len(ids))
	for _, id := range ids {
		opts = append(opts, invoketest.WithKnownGap(id, "SFTP file transfer not implemented yet"))
	}

	return opts
}

// TestSSHContractSuite runs the shared behavioral contracts against the SSH
// provider talking to the embedded server.
func TestSSHContractSuite(t *testing.T) {
	t.Parallel()

	invoketest.Verify(t, func(it invoketest.T) invoke.Environment {
		return dialTestServer(asTestingT(it))
	}, transferGaps()...)
}

func TestConnectionRejectsWrongPassword(t *testing.T) {
	t.Parallel()

	srv := startTestServer(t)

	_, err := ssh.New(srv.host(),
		ssh.WithPort(srv.port()),
		ssh.WithPassword("wrong"),
		ssh.WithHostKeyCallback(xssh.FixedHostKey(srv.hostKey)),
	)
	if err == nil {
		t.Fatal("New with a wrong password succeeded, want an auth failure")
	}
}

func TestConnectionRequiresHostKeyVerification(t *testing.T) {
	t.Parallel()

	srv := startTestServer(t)

	// No known_hosts, no callback, no insecure override: fail closed.
	_, err := ssh.New(srv.host(),
		ssh.WithPort(srv.port()),
		ssh.WithPassword(testPassword),
	)
	if err == nil {
		t.Fatal("New without host-key verification succeeded; it must fail closed")
	}
}

func TestWrongHostKeyIsRejected(t *testing.T) {
	t.Parallel()

	srv := startTestServer(t)
	other := startTestServer(t)

	// Verify against a different server's key: the handshake must fail.
	_, err := ssh.New(srv.host(),
		ssh.WithPort(srv.port()),
		ssh.WithPassword(testPassword),
		ssh.WithHostKeyCallback(xssh.FixedHostKey(other.hostKey)),
	)
	if err == nil {
		t.Fatal("New accepted a mismatched host key")
	}
}

func TestClosedEnvironmentRefusesEverything(t *testing.T) {
	t.Parallel()

	env := dialTestServer(t)
	_ = env.Close()

	ctx := context.Background()

	if _, err := env.Start(ctx, invoke.New("true"), invoke.IO{}); !errors.Is(err, invoke.ErrClosed) {
		t.Errorf("Start after Close = %v, want ErrClosed", err)
	}

	if _, err := env.LookPath(ctx, "sh"); !errors.Is(err, invoke.ErrClosed) {
		t.Errorf("LookPath after Close = %v, want ErrClosed", err)
	}
}
