package ssh_test

import (
	"testing"

	"github.com/ruffel/invoke/ssh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	xssh "golang.org/x/crypto/ssh"
)

// TestTwoHostKeyPoliciesAreRefused checks a hop given more than one
// host-key policy is refused before anything is dialed. Two cannot both
// decide, and the one silently ignored would be the one the caller relied
// on: a left-over insecure override beside a known_hosts file would turn
// verification off without a word.
func TestTwoHostKeyPoliciesAreRefused(t *testing.T) {
	t.Parallel()

	// Unroutable: a refusal that arrives must have come from the check, not
	// from a dial.
	const host = "host.invalid"

	tests := map[string][]ssh.Option{
		"known_hosts and the insecure override": {
			ssh.WithKnownHosts("/nonexistent/known_hosts"), ssh.WithInsecureIgnoreHostKey(),
		},
		"a callback and known_hosts": {
			ssh.WithHostKeyCallback(xssh.InsecureIgnoreHostKey()), ssh.WithKnownHosts("/nonexistent/known_hosts"), //nolint:gosec // Never dialed.
		},
		"a jump host with two": {
			ssh.WithInsecureIgnoreHostKey(),
			ssh.WithJumpHost("jump.invalid", ssh.WithInsecureIgnoreHostKey(), ssh.WithKnownHosts("/nonexistent/known_hosts")),
		},
	}

	for name, opts := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := ssh.New(t.Context(), host, append([]ssh.Option{ssh.WithUser("tester")}, opts...)...)
			require.Error(t, err)
			assert.ErrorContains(t, err, "more than one host-key policy")
		})
	}
}

// TestNewFromConfigVerifiesAgainstKnownHosts checks a Config built by hand
// can name a known_hosts file, as WithKnownHosts does, rather than having
// to hand-build a callback that loses the algorithm constraint.
func TestNewFromConfigVerifiesAgainstKnownHosts(t *testing.T) {
	t.Parallel()

	srv := startTestServer(t, withExtraHostKey(newECDSAHostKey(t)))

	env, err := ssh.NewFromConfig(t.Context(), &ssh.Config{
		Host:           srv.host(),
		Port:           srv.port(),
		User:           "tester",
		Password:       testPassword,
		KnownHostsPath: writeKnownHosts(t, srv, srv.hostKey),
	})
	require.NoError(t, err, "a hand-built Config naming known_hosts")

	t.Cleanup(func() { _ = env.Close() })

	_, err = env.LookPath(t.Context(), "sh")
	assert.NoError(t, err)
}
