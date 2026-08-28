package ssh_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/invoketest"
	"github.com/ruffel/invoke/ssh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	xssh "golang.org/x/crypto/ssh"
)

// jumpOption routes a connection through srv, verifying its host key and
// authenticating with its own credentials.
func jumpOption(srv *testServer, extra ...ssh.Option) ssh.Option {
	opts := []ssh.Option{
		ssh.WithPort(srv.port()),
		ssh.WithUser("tester"),
		ssh.WithPassword(testPassword),
		ssh.WithHostKeyCallback(xssh.FixedHostKey(srv.hostKey)),
	}

	return ssh.WithJumpHost(srv.host(), append(opts, extra...)...)
}

// dialThroughJump connects to target through jump, each hop verified
// against its own host key.
func dialThroughJump(t *testing.T, target, jump *testServer) *ssh.Environment {
	t.Helper()

	env, err := ssh.New(t.Context(), target.host(),
		ssh.WithPort(target.port()),
		ssh.WithUser("tester"),
		ssh.WithPassword(testPassword),
		ssh.WithHostKeyCallback(xssh.FixedHostKey(target.hostKey)),
		jumpOption(jump),
	)
	require.NoError(t, err, "connecting through a jump host")

	t.Cleanup(func() { _ = env.Close() })

	return env
}

// waitForNoConnections waits for a server's accepted connections to settle
// at none, so a teardown that is merely asynchronous is not read as a leak.
func waitForNoConnections(t *testing.T, srv *testServer, what string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if srv.openConnections() == 0 {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	assert.Failf(t, "connections outlived the chain", "%s: %d still open", what, srv.openConnections())
}

// connectWithin runs connect and returns its error, failing the test if it
// does not return within bound.
//
// A bound checked after the call has returned is no bound at all: when the
// mechanism it is testing regresses, the call never returns and the check
// is never reached. The test then fails as a whole-package timeout with a
// goroutine dump, rather than by name.
func connectWithin(t *testing.T, bound time.Duration, connect func() error) error {
	t.Helper()

	done := make(chan error, 1)

	go func() { done <- connect() }()

	select {
	case err := <-done:
		return err
	case <-time.After(bound):
		require.FailNow(t, "connecting was never given up on",
			"no answer within %s; a tunnelled connection has no deadline of its own, so the "+
				"socket at the head of the chain is what has to be closed", bound)

		return nil
	}
}

// startSilentListener accepts connections and says nothing on them, holding
// every socket open: a server that completes a TCP connection and never
// begins the SSH conversation.
func startSilentListener(t *testing.T) (string, int) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0") //nolint:noctx // Test listener.
	require.NoError(t, err, "silent listener")

	t.Cleanup(func() { _ = listener.Close() })

	var (
		mu    sync.Mutex
		conns []net.Conn
	)

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			mu.Lock()

			conns = append(conns, conn)

			mu.Unlock()
		}
	}()

	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()

		for _, conn := range conns {
			_ = conn.Close()
		}
	})

	host, portText, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)

	port, err := strconv.Atoi(portText)
	require.NoError(t, err)

	return host, port
}

// TestJumpHostReachesTheTarget checks the ordinary case: the target runs
// the command, and the jump host carries it without running anything.
func TestJumpHostReachesTheTarget(t *testing.T) {
	t.Parallel()

	jump := startTestServer(t)
	target := startTestServer(t)

	env := dialThroughJump(t, target, jump)

	var out strings.Builder

	proc, err := env.Start(t.Context(), invoke.New("echo", "through"), invoke.IO{Stdout: &out})
	require.NoError(t, err, "Start through a jump host")

	result, err := proc.Wait()
	require.NoError(t, err, "Wait")

	assert.Equal(t, 0, result.ExitCode)
	assert.Equal(t, "through", strings.TrimSpace(out.String()))

	assert.NotEmpty(t, target.recordedExecs(), "the target is what ran the command")
	assert.Empty(t, jump.recordedExecs(), "a jump host carries the connection; it runs nothing")
}

// TestHandBuiltChainConnects checks the escape hatch the Jump field's
// documentation promises.
//
// A Config assembled as a struct cannot express known_hosts verification,
// whose fields are unexported — the documented answer being that options
// are ordinary functions and apply to a hop directly. That is only worth
// documenting if it works, and NewFromConfig is a second entry point that
// could grow its own idea of what a chain is.
func TestHandBuiltChainConnects(t *testing.T) {
	t.Parallel()

	jump := startTestServer(t)
	target := startTestServer(t)

	cfg := &ssh.Config{
		Host:     target.host(),
		Port:     target.port(),
		User:     "tester",
		Password: testPassword,
		Jump: &ssh.Config{
			Host:     jump.host(),
			Port:     jump.port(),
			User:     "tester",
			Password: testPassword,
		},
	}

	// The host-key policy is the part a struct literal cannot state, so
	// each hop gets its own by application.
	ssh.WithKnownHosts(writeKnownHosts(t, target, target.hostKey))(cfg)
	ssh.WithKnownHosts(writeKnownHosts(t, jump, jump.hostKey))(cfg.Jump)

	env, err := ssh.NewFromConfig(t.Context(), cfg)
	require.NoError(t, err, "a hand-built chain must connect like a configured one")

	t.Cleanup(func() { _ = env.Close() })

	_, err = env.LookPath(t.Context(), "sh")
	assert.NoError(t, err, "LookPath through a hand-built chain")

	assert.Positive(t, jump.openForwards(), "the hand-built hop must be the one carrying it")
}

// TestContractSuiteThroughAJumpHost runs the shared behavioral contracts
// over a jumped connection.
//
// A jump changes what carries the connection and nothing above it, so the
// suite has to pass unchanged: anything it catches here is the chain
// leaking into behavior it has no business touching.
func TestContractSuiteThroughAJumpHost(t *testing.T) {
	t.Parallel()

	invoketest.Verify(t, func(it invoketest.T) invoke.Environment {
		tt := asTestingT(it)

		return dialThroughJump(tt, startTestServer(tt), startTestServer(tt))
	})
}

// TestJumpHostIsVerifiedOnItsOwnTerms checks host-key verification is
// per hop, and fails closed on the hop as it does on the target.
//
// This is the property that makes a jump host safe to add: the target
// saying it does not care about host keys must not quietly decide that for
// the machine the connection passes through.
func TestJumpHostIsVerifiedOnItsOwnTerms(t *testing.T) {
	t.Parallel()

	t.Run("an unverified hop is refused", func(t *testing.T) {
		t.Parallel()

		jump := startTestServer(t)
		target := startTestServer(t)

		_, err := ssh.New(t.Context(), target.host(),
			ssh.WithPort(target.port()),
			ssh.WithUser("tester"),
			ssh.WithPassword(testPassword),
			// The target opts out of host-key verification. The hop did
			// not, and must not be opted out along with it.
			ssh.WithInsecureIgnoreHostKey(),
			ssh.WithJumpHost(jump.host(),
				ssh.WithPort(jump.port()),
				ssh.WithUser("tester"),
				ssh.WithPassword(testPassword),
			),
		)
		require.Error(t, err, "an unverified jump host was accepted")

		assert.ErrorContains(t, err, "no host-key verification configured")
		assert.ErrorContains(t, err, "jump "+net.JoinHostPort(jump.host(), strconv.Itoa(jump.port())),
			"the error must say which connection is unverified")
	})

	t.Run("a hop presenting the wrong key is refused", func(t *testing.T) {
		t.Parallel()

		jump := startTestServer(t)
		target := startTestServer(t)
		other := startTestServer(t)

		_, err := ssh.New(t.Context(), target.host(),
			ssh.WithPort(target.port()),
			ssh.WithUser("tester"),
			ssh.WithPassword(testPassword),
			ssh.WithHostKeyCallback(xssh.FixedHostKey(target.hostKey)),
			jumpOption(jump, ssh.WithHostKeyCallback(xssh.FixedHostKey(other.hostKey))),
		)
		require.Error(t, err, "a jump host presenting an unexpected key was accepted")

		assert.ErrorContains(t, err, "jump ", "the failing hop must be named")
	})
}

// TestJumpHostRefusalNamesBothEnds pins the confusing case the naming
// exists for.
//
// A jump host configured not to forward refuses the channel to the target.
// Reported against the target's address alone that reads as the target
// being down — when the target was never contacted at all.
func TestJumpHostRefusalNamesBothEnds(t *testing.T) {
	t.Parallel()

	jump := startTestServer(t, withRefusedForwarding())
	target := startTestServer(t)

	_, err := ssh.New(t.Context(), target.host(),
		ssh.WithPort(target.port()),
		ssh.WithUser("tester"),
		ssh.WithPassword(testPassword),
		ssh.WithHostKeyCallback(xssh.FixedHostKey(target.hostKey)),
		jumpOption(jump),
	)
	require.Error(t, err, "a refused forward must not read as a connection")

	var transportErr *invoke.TransportError

	require.ErrorAs(t, err, &transportErr, "a refused forward is a transport failure")
	assert.Equal(t, "dial", transportErr.Op)

	assert.ErrorContains(t, err, net.JoinHostPort(target.host(), strconv.Itoa(target.port())),
		"the address that could not be reached must be named")
	assert.ErrorContains(t, err, "through jump "+net.JoinHostPort(jump.host(), strconv.Itoa(jump.port())),
		"and so must the connection that would not carry it")

	assert.Equal(t, 1, strings.Count(err.Error(), "transport failure"),
		"a chain reports one transport failure, not one per hop")

	var openErr *xssh.OpenChannelError

	require.ErrorAs(t, err, &openErr,
		"the server's own reason must stay reachable, so policy is distinguishable from weather")
	assert.Equal(t, xssh.Prohibited, openErr.Reason)
}

// TestHandshakeThroughAJumpHostIsBounded is the test the chain's one
// subtle mechanism exists for.
//
// The connection a jump host hands back is a channel, not a socket: it has
// no deadline to set, and closing it only sends a message down the link
// beneath it. So a target that accepts and then says nothing can only be
// given up on by closing the socket at the head of the chain. Without
// that, this hangs — for the configured timeout in the first case, and for
// good in the second.
func TestHandshakeThroughAJumpHostIsBounded(t *testing.T) {
	t.Parallel()

	const (
		// The timeout the target hop is given, and the margin allowed
		// around it. A bound of tens of seconds would be met by a
		// regression that merely waited a very long time.
		handshakeBound = 500 * time.Millisecond
		bound          = 20 * handshakeBound
	)

	t.Run("the target's timeout is honored", func(t *testing.T) {
		t.Parallel()

		jump := startTestServer(t)
		host, port := startSilentListener(t)

		err := connectWithin(t, bound, func() error {
			_, err := ssh.New(t.Context(), host,
				ssh.WithPort(port),
				ssh.WithUser("tester"),
				ssh.WithPassword(testPassword),
				ssh.WithInsecureIgnoreHostKey(),
				ssh.WithTimeout(handshakeBound),
				jumpOption(jump),
			)

			return err
		})
		require.Error(t, err, "a silent target must not read as a connection")

		waitForNoConnections(t, jump, "after a target that never answered")
	})

	// The case that separates closing the right thing from closing the
	// obvious thing. With the link frozen, the channel-close message that
	// gives up on the target is written into a hole: it never arrives, and
	// no answer to it ever comes back. Only closing the socket at the head
	// of the chain ends the read the handshake is blocked on.
	t.Run("a link that dies mid-handshake does not hang it", func(t *testing.T) {
		t.Parallel()

		jump := startTestServer(t)
		link := startFrozenLink(t, jump.addr)
		host, port := startSilentListener(t)

		// Long enough that the chain is up and the target handshake is
		// blocked before the link stops carrying anything.
		go func() {
			time.Sleep(handshakeBound)
			link.freeze()
		}()

		err := connectWithin(t, bound, func() error {
			_, err := ssh.New(t.Context(), host,
				ssh.WithPort(port),
				ssh.WithUser("tester"),
				ssh.WithPassword(testPassword),
				ssh.WithInsecureIgnoreHostKey(),
				ssh.WithTimeout(4*handshakeBound),
				ssh.WithJumpHost(link.host(),
					ssh.WithPort(link.port()),
					ssh.WithUser("tester"),
					ssh.WithPassword(testPassword),
					ssh.WithHostKeyCallback(xssh.FixedHostKey(jump.hostKey)),
				),
			)

			return err
		})
		require.Error(t, err, "a silent target behind a dead link must not read as a connection")
	})

	t.Run("cancellation interrupts it", func(t *testing.T) {
		t.Parallel()

		jump := startTestServer(t)
		host, port := startSilentListener(t)

		ctx, cancel := context.WithCancel(t.Context())

		go func() {
			time.Sleep(100 * time.Millisecond)
			cancel()
		}()

		err := connectWithin(t, bound, func() error {
			_, err := ssh.New(ctx, host,
				ssh.WithPort(port),
				ssh.WithUser("tester"),
				ssh.WithPassword(testPassword),
				ssh.WithInsecureIgnoreHostKey(),
				// Long enough that only the cancellation can end this.
				ssh.WithTimeout(time.Minute),
				jumpOption(jump),
			)

			return err
		})
		require.Error(t, err, "a canceled setup must not connect")

		assert.ErrorIs(t, err, context.Canceled,
			"the caller's own cancellation is the cause worth reporting")

		assert.ErrorContains(t, err, "jump ",
			"a chain cut short must still say which connection was being made")
	})
}

// TestFailedTargetLeavesNothingBehind checks a chain that could not be
// finished does not outlive the attempt: the hop that was established is
// torn down with it.
func TestFailedTargetLeavesNothingBehind(t *testing.T) {
	t.Parallel()

	jump := startTestServer(t)
	target := startTestServer(t)
	other := startTestServer(t)

	// The jump host is reached and authenticated; the target then presents
	// a key that is not the one expected.
	_, err := ssh.New(t.Context(), target.host(),
		ssh.WithPort(target.port()),
		ssh.WithUser("tester"),
		ssh.WithPassword(testPassword),
		ssh.WithHostKeyCallback(xssh.FixedHostKey(other.hostKey)),
		jumpOption(jump),
	)
	require.Error(t, err, "a target presenting an unexpected key was accepted")

	waitForNoConnections(t, jump, "after the target failed")

	assert.Eventually(t, func() bool { return jump.openForwards() == 0 }, 5*time.Second, 10*time.Millisecond,
		"the forwarded channel outlived the connection it was carrying")
}

// TestCloseTearsDownEveryHop checks Close reaches the whole chain, not
// only the target: a jump host's connection left behind would hold a
// socket for the life of the process.
func TestCloseTearsDownEveryHop(t *testing.T) {
	t.Parallel()

	jump := startTestServer(t)
	target := startTestServer(t)

	env := dialThroughJump(t, target, jump)

	_, err := env.LookPath(t.Context(), "sh")
	require.NoError(t, err, "the chain must be usable before it is torn down")

	require.NoError(t, env.Close())

	waitForNoConnections(t, target, "the target after Close")
	waitForNoConnections(t, jump, "the jump host after Close")
}

// TestDeadTargetClosesTheWholeChain checks the keepalive's discovery
// reaches every hop. A probe that goes unanswered says the chain is dead,
// and closing only the target would leave the jump host's socket held open
// until something else noticed.
func TestDeadTargetClosesTheWholeChain(t *testing.T) {
	t.Parallel()

	const probeInterval = 100 * time.Millisecond

	jump := startTestServer(t)
	target := startTestServer(t)

	env, err := ssh.New(t.Context(), target.host(),
		ssh.WithPort(target.port()),
		ssh.WithUser("tester"),
		ssh.WithPassword(testPassword),
		ssh.WithHostKeyCallback(xssh.FixedHostKey(target.hostKey)),
		ssh.WithKeepAlive(probeInterval),
		jumpOption(jump),
	)
	require.NoError(t, err)

	t.Cleanup(func() { _ = env.Close() })

	target.blackholeNow()

	// Nothing is closed by this test: the probe loop is what must notice,
	// and what it closes is the chain rather than the target alone.
	waitForNoConnections(t, jump, "the jump host after the target died")
}

// TestSilentlyDeadJumpHostIsDiscovered pins why one probe loop is enough
// for a whole chain.
//
// A probe to the target crosses every hop on the way, so a link that dies
// silently part way along strands it exactly as a dead target does. The
// discovery has to happen on the same interval, or a dead jump host looks
// like a target that is merely slow.
func TestSilentlyDeadJumpHostIsDiscovered(t *testing.T) {
	t.Parallel()

	const (
		probeInterval = 100 * time.Millisecond
		bound         = 8 * time.Second
	)

	jump := startTestServer(t)
	target := startTestServer(t)

	// The link to the jump host, which the test can stop carrying traffic
	// without closing anything.
	link := startFrozenLink(t, jump.addr)

	env, err := ssh.New(t.Context(), target.host(),
		ssh.WithPort(target.port()),
		ssh.WithUser("tester"),
		ssh.WithPassword(testPassword),
		ssh.WithHostKeyCallback(xssh.FixedHostKey(target.hostKey)),
		ssh.WithKeepAlive(probeInterval),
		ssh.WithJumpHost(link.host(),
			ssh.WithPort(link.port()),
			ssh.WithUser("tester"),
			ssh.WithPassword(testPassword),
			ssh.WithHostKeyCallback(xssh.FixedHostKey(jump.hostKey)),
		),
	)
	require.NoError(t, err)

	t.Cleanup(func() { _ = env.Close() })

	proc, err := env.Start(t.Context(), invoke.New("sleep", "30"), invoke.IO{})
	require.NoError(t, err)

	t.Cleanup(func() { _ = proc.Close() })

	link.freeze()

	waited := make(chan error, 1)

	go func() {
		_, waitErr := proc.Wait()
		waited <- waitErr
	}()

	select {
	case waitErr := <-waited:
		require.Error(t, waitErr, "a dead chain cannot end in a reported success")

		var exitErr *invoke.ExitError

		assert.NotErrorAs(t, waitErr, &exitErr,
			"no status arrived; the outcome must not read as the command's own exit")
	case <-time.After(bound):
		t.Fatal("Wait did not return: the silently dead jump host was never discovered")
	}

	closed := make(chan struct{})

	go func() {
		_ = env.Close()

		close(closed)
	}()

	select {
	case <-closed:
	case <-time.After(bound):
		t.Fatal("Close did not return through a silently dead jump host")
	}
}

// TestChainOfTwoJumpHosts checks a chain longer than one hop works, and
// that each hop carries the next rather than the target being reached some
// other way.
func TestChainOfTwoJumpHosts(t *testing.T) {
	t.Parallel()

	first := startTestServer(t)
	second := startTestServer(t)
	target := startTestServer(t)

	// ssh -J first,second target
	env, err := ssh.New(t.Context(), target.host(),
		ssh.WithPort(target.port()),
		ssh.WithUser("tester"),
		ssh.WithPassword(testPassword),
		ssh.WithHostKeyCallback(xssh.FixedHostKey(target.hostKey)),
		jumpOption(first),
		jumpOption(second),
	)
	require.NoError(t, err, "connecting through two jump hosts")

	t.Cleanup(func() { _ = env.Close() })

	var out strings.Builder

	proc, err := env.Start(t.Context(), invoke.New("echo", "two hops"), invoke.IO{Stdout: &out})
	require.NoError(t, err)

	_, err = proc.Wait()
	require.NoError(t, err)

	assert.Equal(t, "two hops", strings.TrimSpace(out.String()))

	assert.Positive(t, first.openForwards(), "the first hop must be carrying the second")
	assert.Positive(t, second.openForwards(), "and the second the target")

	require.NoError(t, env.Close())

	waitForNoConnections(t, first, "the first hop after Close")
	waitForNoConnections(t, second, "the second hop after Close")
	waitForNoConnections(t, target, "the target after Close")
}

// TestFailureMidChainNamesTheHopAndCleansUp checks a chain that fails part
// way along says where, and leaves nothing established behind it.
func TestFailureMidChainNamesTheHopAndCleansUp(t *testing.T) {
	t.Parallel()

	first := startTestServer(t)
	second := startTestServer(t, withRefusedForwarding())
	target := startTestServer(t)

	_, err := ssh.New(t.Context(), target.host(),
		ssh.WithPort(target.port()),
		ssh.WithUser("tester"),
		ssh.WithPassword(testPassword),
		ssh.WithHostKeyCallback(xssh.FixedHostKey(target.hostKey)),
		jumpOption(first),
		jumpOption(second),
	)
	require.Error(t, err, "the second hop refuses to forward; the chain cannot be built")

	assert.ErrorContains(t, err, "through jump "+net.JoinHostPort(second.host(), strconv.Itoa(second.port())),
		"the hop that refused must be the one named")

	waitForNoConnections(t, first, "the first hop after the second refused")
}

// TestEveryHopReleasesItsAgentSocket checks a hop authenticating through
// the agent opens its own socket, and that every one of them is released —
// whether the chain was built and then closed, or never finished at all.
//
// The sockets are held open for the life of the connection, agent
// authentication signing on demand, so one left behind leaks for the life
// of the process. The half-built case is the easier one to get wrong: a
// hop's socket is opened before its transport, so the hop that fails has
// one to release even though it never connected.
func TestEveryHopReleasesItsAgentSocket(t *testing.T) {
	// SSH_AUTH_SOCK is process-global, so these cannot run alongside
	// anything else that sets it — including each other.
	for _, tc := range []struct {
		name      string
		targetKey func(target, other *testServer) xssh.PublicKey
		connects  bool
	}{
		{
			name:      "the chain is closed",
			targetKey: func(target, _ *testServer) xssh.PublicKey { return target.hostKey },
			connects:  true,
		},
		{
			name: "the chain is never finished",
			// The jump host is reached and authenticated; the target then
			// presents a key that is not the one expected, so the chain
			// fails with two agent sockets open.
			targetKey: func(_, other *testServer) xssh.PublicKey { return other.hostKey },
			connects:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agentPath, released := startCountingAgent(t)

			t.Setenv("SSH_AUTH_SOCK", agentPath)

			jump := startTestServer(t)
			target := startTestServer(t)
			other := startTestServer(t)

			env, err := ssh.New(t.Context(), target.host(),
				ssh.WithPort(target.port()),
				ssh.WithUser("tester"),
				ssh.WithPassword(testPassword),
				ssh.WithAgent(),
				ssh.WithHostKeyCallback(xssh.FixedHostKey(tc.targetKey(target, other))),
				jumpOption(jump, ssh.WithAgent()),
			)

			if tc.connects {
				require.NoError(t, err)
				require.NoError(t, env.Close())
			} else {
				require.Error(t, err, "a target presenting an unexpected key was accepted")
			}

			// One for the jump host, one for the target.
			const hops = 2

			for count := range hops {
				select {
				case <-released:
				case <-time.After(5 * time.Second):
					require.Failf(t, "an agent socket outlived the chain",
						"%d of %d hops released theirs", count, hops)
				}
			}
		})
	}
}

// startCountingAgent listens on an agent socket that never answers, and
// reports on the returned channel every time a client lets one go.
func startCountingAgent(t *testing.T) (string, <-chan struct{}) {
	t.Helper()

	// A unix socket path is limited to about 100 bytes, which the usual
	// per-test temp directory can exceed.
	//nolint:usetesting // t.TempDir can exceed the unix socket path limit.
	dir, err := os.MkdirTemp("", "iv")
	require.NoError(t, err, "temp dir")

	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	path := filepath.Join(dir, "a.sock")

	listener, err := net.Listen("unix", path) //nolint:noctx // Local test socket; no context to bound.
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}

	t.Cleanup(func() { _ = listener.Close() })

	released := make(chan struct{}, 8)

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			go func() {
				// The server offers password authentication only, so the
				// agent's keys are never asked for and nothing is ever
				// written here. Reading returns when the client lets go,
				// which is the event being counted — so a server that grew
				// public-key authentication would make this vacuous.
				buf := make([]byte, 1)
				_, _ = conn.Read(buf)
				_ = conn.Close()

				released <- struct{}{}
			}()
		}
	}()

	return path, released
}

// frozenLink is a TCP relay that can be told to stop carrying traffic
// without closing anything, standing in for a link that dies silently: the
// sockets stay up, writes are still accepted, and no answer ever comes.
type frozenLink struct {
	addr   string
	frozen atomic.Bool
}

// startFrozenLink relays connections to addr until it is frozen.
func startFrozenLink(t *testing.T, to string) *frozenLink {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0") //nolint:noctx // Test listener.
	require.NoError(t, err, "relay listener")

	t.Cleanup(func() { _ = listener.Close() })

	link := &frozenLink{addr: listener.Addr().String()}

	var (
		mu    sync.Mutex
		conns []net.Conn
	)

	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()

		for _, conn := range conns {
			_ = conn.Close()
		}
	})

	go func() {
		for {
			client, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			server, dialErr := net.Dial("tcp", to) //nolint:noctx // Test relay; lifetime is the connection.
			if dialErr != nil {
				_ = client.Close()

				continue
			}

			mu.Lock()

			conns = append(conns, client, server)

			mu.Unlock()

			go link.relay(client, server)
			go link.relay(server, client)
		}
	}()

	return link
}

// relay copies until the link is frozen, after which it keeps reading and
// throws away what it reads. The sender sees its writes accepted, and
// nothing it sends is ever answered — which is what a black hole looks
// like from either end.
func (l *frozenLink) relay(from, to net.Conn) {
	buf := make([]byte, 32*1024)

	for {
		n, err := from.Read(buf)

		if n > 0 && !l.frozen.Load() {
			if _, writeErr := to.Write(buf[:n]); writeErr != nil {
				return
			}
		}

		if err != nil {
			return
		}
	}
}

func (l *frozenLink) freeze() {
	l.frozen.Store(true)
}

func (l *frozenLink) host() string {
	host, _, _ := net.SplitHostPort(l.addr)

	return host
}

func (l *frozenLink) port() int {
	_, portText, _ := net.SplitHostPort(l.addr)
	port, _ := strconv.Atoi(portText)

	return port
}
