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
	"crypto/rand"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/invoketest"
	"github.com/ruffel/invoke/ssh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	xssh "golang.org/x/crypto/ssh"
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

	// containerLogTimeout bounds each attempt to read a failed lane's
	// log. Separate from the teardown budget on purpose: the container
	// this is asked about has just failed a test, so it is exactly the
	// one that might not answer, and evidence that cannot be collected
	// must not cost the removal that follows it.
	containerLogTimeout = 15 * time.Second

	// containerLogLines is how much of a failing lane's server log is
	// worth reading: enough to cover one contract's exchange, not the
	// whole suite's.
	containerLogLines = 200

	// sshdLogPath is where the server writes that log inside the
	// container.
	sshdLogPath = "/tmp/sshd.log"
)

// allowForwarding and refuseForwarding are a jump host's forwarding
// policy, stated explicitly in both directions.
//
// Explicitly, because a jump host carries nothing unless it is configured
// to, and the stock answer differs between distributions: the image these
// tests use refuses forwarding out of the box. Depending on which default
// an image happens to ship would make this lane test the image rather than
// the provider.
const (
	allowForwarding  = "sed -i 's/^#*AllowTcpForwarding.*/AllowTcpForwarding yes/' /etc/ssh/sshd_config && "
	refuseForwarding = "sed -i 's/^#*AllowTcpForwarding.*/AllowTcpForwarding no/' /etc/ssh/sshd_config && "
)

// sshdSetup is the command that brings up a server, with extra inserted
// into its configuration before it starts.
//
// Deliberately stock otherwise: the point is to meet the configuration a
// user would actually find, including which environment variables it is
// willing to accept and whether it forwards.
func sshdSetup(extra string) string {
	return "apk add --no-cache openssh >/dev/null 2>&1 && " +
		"ssh-keygen -A >/dev/null 2>&1 && " +
		"echo '" + opensshUser + ":" + opensshPassword + "' | chpasswd && " +
		"sed -i 's/^#*PermitRootLogin.*/PermitRootLogin yes/' /etc/ssh/sshd_config && " +
		extra +
		// The log goes to a file, not stderr: a session's stderr is the
		// caller's own stream, so -e puts the server's debug output in
		// the output of every command the contracts run. DEBUG makes it
		// say what it did with each channel request; a failing test
		// dumps the tail of it, and nothing reads it otherwise.
		"/usr/sbin/sshd -D -E " + sshdLogPath + " -o LogLevel=DEBUG"
}

// startContainer runs a container with the given arguments and removes it
// when the test finishes.
//
// The container runtime is driven through its own command line rather
// than a client library, so this stays free of the daemon-location
// problem and of any dependency the provider itself does not need.
func startContainer(tb testing.TB, args ...string) string {
	tb.Helper()

	ctx, cancel := context.WithTimeout(tb.Context(), containerStartTimeout)
	defer cancel()

	//nolint:gosec // The arguments are literals and names this test generated.
	out, err := exec.CommandContext(ctx, "docker",
		append([]string{"run", "-d", "--rm"}, args...)...).Output()
	require.NoError(tb, err, "starting the container")

	id := strings.TrimSpace(string(out))

	tb.Cleanup(func() {
		reportContainerLog(tb, id)

		removeCtx, removeCancel := context.WithTimeout(context.Background(), containerStopTimeout)
		defer removeCancel()

		//nolint:gosec // The argument is a container id this function just created.
		_ = exec.CommandContext(removeCtx, "docker", "rm", "-f", id).Run()
	})

	return id
}

// reportContainerLog writes the container's own log into the test output
// when the test has failed.
//
// These lanes exist to catch what only a real server does, and when one
// of them fails on a machine nobody can attach to, the server's account
// of the exchange is the evidence that is otherwise lost: the client side
// alone cannot say whether a request arrived and was refused or never
// arrived at all.
// It owns its budget rather than taking one, so that a container which
// will not answer cannot spend the teardown that follows: an expired
// context makes exec skip the command entirely, and the container would
// outlive the run and hold the private network with it.
func reportContainerLog(tb testing.TB, id string) {
	tb.Helper()

	if !tb.Failed() {
		return
	}

	tail := strconv.Itoa(containerLogLines)

	out, err := runBounded(tb, "docker", "exec", id, "tail", "-n", tail, sshdLogPath)
	if err != nil {
		// Not an sshd container, or it never got far enough to write a
		// log: whatever it put on its own output is the next best
		// account. Its own budget, so the attempt above cannot have
		// spent this one.
		out, err = runBounded(tb, "docker", "logs", "--tail", tail, id)
	}

	if err != nil {
		tb.Logf("could not read the log of container %s: %v", id, err)

		return
	}

	tb.Logf("last %s lines of the log of container %s:\n%s", tail, id, out)
}

// runBounded runs one diagnostic command under its own deadline.
func runBounded(tb testing.TB, name string, args ...string) ([]byte, error) {
	tb.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), containerLogTimeout)
	defer cancel()

	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// startOpenSSH launches a container running sshd with its stock
// configuration and returns the port it is reachable on.
func startOpenSSH(tb testing.TB) int {
	tb.Helper()

	id := startContainer(tb, "-p", "127.0.0.1::22", opensshImage, "sh", "-c", sshdSetup(""))

	return waitForSSHD(tb, id)
}

// waitForSSHD resolves the container's published port and waits until the
// server completes a handshake on it.
func waitForSSHD(tb testing.TB, id string) int {
	tb.Helper()

	port := publishedPort(tb, id)

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		env, err := ssh.New(tb.Context(), "127.0.0.1",
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

	require.FailNow(tb, "sshd did not become reachable within 90s")

	return 0
}

// publishedPort waits for the runtime to publish the container's ssh port
// and returns it.
func publishedPort(tb testing.TB, id string) int {
	tb.Helper()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.CommandContext(tb.Context(), "docker", "port", id, "22/tcp").Output()
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

	require.FailNow(tb, "the container never published its ssh port")

	return 0
}

// dialOpenSSH connects the provider to the containerized server.
func dialOpenSSH(tb testing.TB, port int, opts ...ssh.Option) *ssh.Environment {
	tb.Helper()

	base := []ssh.Option{
		ssh.WithPort(port),
		ssh.WithUser(opensshUser),
		ssh.WithPassword(opensshPassword),
		ssh.WithInsecureIgnoreHostKey(),
	}

	env, err := ssh.New(tb.Context(), "127.0.0.1", append(base, opts...)...)
	require.NoError(tb, err, "connecting to the containerized sshd")

	tb.Cleanup(func() { _ = env.Close() })

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
	})
}

// bastionTopology is a target reachable only through a jump host, which is
// the arrangement jump support exists for.
type bastionTopology struct {
	// jumpPort is the jump host's published port on the loopback address,
	// and the only way into the topology from here.
	jumpPort int

	// targetName is the target's address on the private network. It
	// resolves from the jump host and nowhere else.
	targetName string
}

// options connect to the target through the jump host, each hop
// authenticated on its own.
func (b bastionTopology) options(extra ...ssh.Option) []ssh.Option {
	opts := []ssh.Option{
		ssh.WithUser(opensshUser),
		ssh.WithPassword(opensshPassword),
		ssh.WithInsecureIgnoreHostKey(),
		ssh.WithJumpHost("127.0.0.1",
			ssh.WithPort(b.jumpPort),
			ssh.WithUser(opensshUser),
			ssh.WithPassword(opensshPassword),
			ssh.WithInsecureIgnoreHostKey(),
		),
	}

	return append(opts, extra...)
}

// startBastionTopology brings up a jump host reachable from here and a
// target reachable only from the jump host.
//
// The target publishes no port and lives on a network of its own, so its
// name resolves through the runtime's embedded DNS from a container
// attached to that network and from nowhere else. A provider that
// regressed to dialing the target directly could not reach it at all,
// which is what makes the second container worth its cost.
//
// The network is user-defined but not internal. Both containers install
// sshd from the package repository when they start, which an internal
// network would cut them off from; the isolation comes from the target
// publishing no port rather than from the network refusing egress.
func startBastionTopology(tb testing.TB, jumpExtra string) bastionTopology {
	tb.Helper()

	network := createNetwork(tb)

	targetName := uniqueName(tb, "target")

	startContainer(tb,
		"--network", network,
		"--name", targetName,
		opensshImage, "sh", "-c", sshdSetup(""))

	jumpID := startContainer(tb,
		"-p", "127.0.0.1::22",
		opensshImage, "sh", "-c", sshdSetup(jumpExtra))

	connectNetwork(tb, network, jumpID)

	return bastionTopology{jumpPort: waitForSSHD(tb, jumpID), targetName: targetName}
}

// createNetwork makes a private network for one topology and removes it
// when the test finishes.
func createNetwork(tb testing.TB) string {
	tb.Helper()

	name := uniqueName(tb, "net")

	ctx, cancel := context.WithTimeout(tb.Context(), containerStopTimeout)
	defer cancel()

	//nolint:gosec // The name was generated by this test.
	require.NoError(tb, exec.CommandContext(ctx, "docker", "network", "create", name).Run(),
		"creating the private network")

	// Registered before any container joins it. Cleanup runs in reverse,
	// so every container is gone by the time the network is removed —
	// the only order the runtime accepts.
	tb.Cleanup(func() {
		removeCtx, removeCancel := context.WithTimeout(context.Background(), containerStopTimeout)
		defer removeCancel()

		//nolint:gosec // The name was generated by this test.
		_ = exec.CommandContext(removeCtx, "docker", "network", "rm", name).Run()
	})

	return name
}

// connectNetwork attaches a running container to the private network.
//
// The jump host joins after it has started rather than at run time:
// attaching several networks in one run needs a newer engine than
// attaching one and connecting the rest.
func connectNetwork(tb testing.TB, network, id string) {
	tb.Helper()

	ctx, cancel := context.WithTimeout(tb.Context(), containerStopTimeout)
	defer cancel()

	require.NoError(tb, exec.CommandContext(ctx, "docker", "network", "connect", network, id).Run(),
		"attaching the jump host to the private network")
}

// uniqueName names a runtime object so parallel tests cannot collide.
func uniqueName(tb testing.TB, kind string) string {
	tb.Helper()

	var raw [6]byte

	_, err := rand.Read(raw[:])
	require.NoError(tb, err, "unique name")

	return fmt.Sprintf("invoke-%s-%x", kind, raw)
}

// waitForTargetBehindJump waits until the target answers through the jump
// host, and then checks it answers nowhere else.
//
// The second half guards the guard. The topology is only evidence that the
// jump host is being used if the target cannot be reached without it, and
// a change that published the target's port for convenience would let a
// regression to direct dialing pass unnoticed.
func waitForTargetBehindJump(tb testing.TB, topo bastionTopology) {
	tb.Helper()

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		env, err := ssh.New(tb.Context(), topo.targetName, topo.options()...)
		if err == nil {
			_ = env.Close()

			_, direct := ssh.New(tb.Context(), topo.targetName,
				ssh.WithUser(opensshUser),
				ssh.WithPassword(opensshPassword),
				ssh.WithInsecureIgnoreHostKey(),
				ssh.WithTimeout(10*time.Second),
			)
			require.Error(tb, direct,
				"the target answered a direct dial: it is not actually behind the jump host, "+
					"so nothing here would notice a regression to dialing it directly")

			return
		}

		time.Sleep(500 * time.Millisecond)
	}

	require.FailNow(tb, "the target did not become reachable through the jump host within 90s")
}

// TestOpenSSHJumpContractSuite runs the shared contracts against a real
// server reached through a real jump host.
//
// The contracts know nothing about how the connection is carried, so they
// have to pass exactly as they do against a direct one. What this adds
// over the in-process chain is a real sshd doing the forwarding, with its
// own idea of what a forwarded channel is worth.
func TestOpenSSHJumpContractSuite(t *testing.T) {
	t.Parallel()

	topo := startBastionTopology(t, allowForwarding)
	waitForTargetBehindJump(t, topo)

	invoketest.Verify(t, func(it invoketest.T) invoke.Environment {
		tt, ok := it.(*testing.T)
		require.True(tt, ok, "contract tests require the standard *testing.T")

		env, err := ssh.New(tt.Context(), topo.targetName, topo.options()...)
		require.NoError(tt, err, "connecting to the target through the jump host")

		tt.Cleanup(func() { _ = env.Close() })

		return env
	})
}

// TestOpenSSHRefusedForwardingIsReported checks what a real server does
// when it is configured not to forward at all.
//
// The in-process server answers this case with a refusal these tests
// wrote themselves, which proves only that the classification matches the
// imitation. Only a real sshd can say what the refusal actually looks
// like.
func TestOpenSSHRefusedForwardingIsReported(t *testing.T) {
	t.Parallel()

	topo := startBastionTopology(t, refuseForwarding)

	_, err := ssh.New(t.Context(), topo.targetName, topo.options()...)
	require.Error(t, err, "a jump host that refuses to forward cannot carry a connection")

	var transportErr *invoke.TransportError

	require.ErrorAs(t, err, &transportErr, "a refused forward is a transport failure")

	assert.ErrorContains(t, err, "through jump",
		"the error must name the connection that refused, not just the one that was wanted")

	var openErr *xssh.OpenChannelError

	require.ErrorAs(t, err, &openErr, "the server's own reason must stay reachable")
	assert.Equal(t, xssh.Prohibited, openErr.Reason,
		"a server refusing on policy grounds must be distinguishable from one that could not connect")
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
