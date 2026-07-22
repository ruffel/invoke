//go:build openssh

// The contract suite normally runs the SSH provider against an in-process
// server written for these tests. That server implements what this
// package believes the protocol does, so it cannot reveal where that
// belief is wrong.
//
// These tests run the same suite against a real OpenSSH server in a
// container, with its own default configuration. They need a container
// runtime, so they are behind the "openssh" build tag and run in the
// integration lane.
package ssh_test

import (
	"context"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/invoketest"
	"github.com/ruffel/invoke/ssh"
	"github.com/stretchr/testify/require"
)

const (
	// opensshImage carries a real sshd and the utilities the contracts
	// exercise.
	opensshImage = "alpine:3"

	// opensshUser and opensshPassword are the credentials the container
	// is set up with.
	opensshUser     = "root"
	opensshPassword = "testpass"

	// containerStartTimeout bounds bringing the server up, which includes
	// fetching the image on a cold machine.
	containerStartTimeout = 5 * time.Minute

	// containerStopTimeout bounds tearing it down again.
	containerStopTimeout = time.Minute
)

// startOpenSSH launches a container running sshd with its stock
// configuration and returns the port it is reachable on.
//
// The container runtime is driven through its own command line rather
// than a client library, so this stays free of the daemon-location
// problem and of any dependency the provider itself does not need.
func startOpenSSH(t *testing.T) int {
	t.Helper()

	// Deliberately stock: the point is to meet the configuration a user
	// would actually find, including which environment variables it is
	// willing to accept.
	setup := "apk add --no-cache openssh >/dev/null 2>&1 && " +
		"ssh-keygen -A >/dev/null 2>&1 && " +
		"echo '" + opensshUser + ":" + opensshPassword + "' | chpasswd && " +
		"sed -i 's/^#*PermitRootLogin.*/PermitRootLogin yes/' /etc/ssh/sshd_config && " +
		"/usr/sbin/sshd -D"

	ctx, cancel := context.WithTimeout(t.Context(), containerStartTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "docker", "run", "-d", "--rm",
		"-p", "127.0.0.1::22", opensshImage, "sh", "-c", setup).Output()
	require.NoError(t, err, "starting the sshd container")

	id := strings.TrimSpace(string(out))

	t.Cleanup(func() {
		removeCtx, removeCancel := context.WithTimeout(context.Background(), containerStopTimeout)
		defer removeCancel()

		//nolint:gosec // The argument is a container id this function just created.
		_ = exec.CommandContext(removeCtx, "docker", "rm", "-f", id).Run()
	})

	return waitForSSHD(t, id)
}

// waitForSSHD resolves the container's published port and waits until the
// server completes a handshake on it.
func waitForSSHD(t *testing.T, id string) int {
	t.Helper()

	port := publishedPort(t, id)

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		env, err := ssh.New(t.Context(), "127.0.0.1",
			ssh.WithPort(port),
			ssh.WithUser(opensshUser),
			ssh.WithPassword(opensshPassword),
			ssh.WithInsecureIgnoreHostKey(),
		)
		if err == nil {
			_ = env.Close()

			return port
		}

		time.Sleep(500 * time.Millisecond)
	}

	require.FailNow(t, "sshd did not become reachable within 90s")

	return 0
}

// publishedPort waits for the runtime to publish the container's ssh port
// and returns it.
func publishedPort(t *testing.T, id string) int {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.CommandContext(t.Context(), "docker", "port", id, "22/tcp").Output()
		if err == nil {
			mapped := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])

			if _, portStr, splitErr := net.SplitHostPort(mapped); splitErr == nil {
				if port, convErr := strconv.Atoi(portStr); convErr == nil {
					return port
				}
			}
		}

		time.Sleep(250 * time.Millisecond)
	}

	require.FailNow(t, "the container never published its ssh port")

	return 0
}

// dialOpenSSH connects the provider to the containerized server.
func dialOpenSSH(t *testing.T, port int, opts ...ssh.Option) *ssh.Environment {
	t.Helper()

	base := []ssh.Option{
		ssh.WithPort(port),
		ssh.WithUser(opensshUser),
		ssh.WithPassword(opensshPassword),
		ssh.WithInsecureIgnoreHostKey(),
	}

	env, err := ssh.New(t.Context(), "127.0.0.1", append(base, opts...)...)
	require.NoError(t, err, "connecting to the containerized sshd")

	t.Cleanup(func() { _ = env.Close() })

	return env
}

// TestOpenSSHContractSuite runs the shared behavioral contracts against a
// real server, which is the only way to find where the in-process one
// differs from the thing it stands in for.
//
// Nothing is opted into: a stock server accepts no environment variables
// out of band, so the environment contracts passing here is the proof
// that the default delivery route works against a real server.
func TestOpenSSHContractSuite(t *testing.T) {
	t.Parallel()

	port := startOpenSSH(t)

	invoketest.Verify(t, func(it invoketest.T) invoke.Environment {
		tt, ok := it.(*testing.T)
		require.True(tt, ok, "contract tests require the standard *testing.T")

		return dialOpenSSH(tt, port)
	}, invoketest.WithKnownGap("lifecycle/cancel-during-drain-keeps-outcome",
		"the outcome is read from the context before the session's own exit status, so a cancellation "+
			"arriving while output drains rewrites a completed exit; fixed separately"))
}

// TestOpenSSHEnvFallbackDelivers checks the opt-in fallback does deliver
// the variable to a server that refuses the out-of-band route.
func TestOpenSSHEnvFallbackDelivers(t *testing.T) {
	t.Parallel()

	port := startOpenSSH(t)
	env := dialOpenSSH(t, port, ssh.WithCommandLineEnv())

	cmd := invoke.New("printenv", "TOKEN")
	cmd.Env = []string{"TOKEN=secret-value"}

	var out strings.Builder

	proc, err := env.Start(t.Context(), cmd, invoke.IO{Stdout: &out})
	require.NoError(t, err, "Start with the command-line fallback enabled")

	result, waitErr := proc.Wait()
	require.NoErrorf(t, waitErr, "exit=%d", result.ExitCode)

	require.Equal(t, "secret-value", strings.TrimSpace(out.String()),
		"the fallback must actually deliver the variable")
}
