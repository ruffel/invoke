package ssh

import (
	"context"
	"fmt"
	"io"
	"net"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ruffel/invoke"
	"golang.org/x/crypto/ssh"
)

// chain is the sequence of connections a target is reached through,
// outermost first: hops[0] is dialed from this process and holds the only
// real socket, and every hop after it rides inside the one before. A
// direct connection is a chain of one.
type chain struct {
	hops []hop

	closeOnce sync.Once
	closeErr  error
}

// hop is one established connection, together with the agent socket its
// authentication holds open. Agent authentication signs on demand, so the
// socket lives as long as the connection does.
type hop struct {
	client *ssh.Client
	agent  io.Closer
}

// Close tears the chain down, outermost first, and releases every agent
// socket it holds.
//
// The order is not the intuitive one, and it is deliberate. Only the
// outermost client owns a socket; every inner one is a channel riding on
// the hop before it, and closing a channel means writing a message over
// that hop — the very link that may be why the chain is being closed. Such
// a write can block behind a send buffer nobody is draining, and closing
// from the target inwards would then hold Close open on a dead connection:
// exactly the hang the keepalive exists to prevent. Closing the socket
// cannot block, and it ends every channel above it.
//
// Nothing is given up for that. A client is closed by dropping its
// transport rather than by saying goodbye, so each server sees the same
// abrupt close whichever order the chain is torn down in.
func (c *chain) Close() error {
	c.closeOnce.Do(func() {
		for i, h := range c.hops {
			err := h.client.Close()

			// The outermost close is the one worth reporting: it owns the
			// socket. An inner one failing afterwards says only that the
			// transport it would have spoken over is already gone.
			if i == 0 {
				c.closeErr = err
			}
		}

		for _, h := range c.hops {
			closeAgent(h.agent)
		}
	})

	return c.closeErr
}

// last returns the most recently established client: the hop a further one
// is dialed through while the chain is being built, and the target once it
// is complete. It is nil before the first hop.
func (c *chain) last() *ssh.Client {
	if len(c.hops) == 0 {
		return nil
	}

	return c.hops[len(c.hops)-1].client
}

// target returns the far end of the chain — the host the caller asked for,
// and the client every session runs on.
func (c *chain) target() *ssh.Client {
	return c.last()
}

// outermost returns the client holding the raw socket, or nil before any
// hop is established.
func (c *chain) outermost() *ssh.Client {
	if len(c.hops) == 0 {
		return nil
	}

	return c.hops[0].client
}

// abort unblocks a handshake whose context has ended.
//
// For the first hop that is closing its socket, which errors the read the
// handshake is waiting on. For a hop reached through another it cannot be:
// that connection is a channel, and closing a channel only sends a message
// down the link beneath it — the very link that may be why the handshake is
// stuck, and a message that can block behind a full send buffer. What ends
// the read is the transport underneath dying, so the socket at the head of
// the chain is what gets closed, before the channel is closed for
// tidiness. The half-built chain is abandoned either way: establishing it
// has already failed.
func (c *chain) abort(conn net.Conn) {
	if head := c.outermost(); head != nil {
		_ = head.Close()
	}

	_ = conn.Close()
}

// establish dials and authenticates every hop, from the one this process
// can reach inwards to the target, and returns the chain ready for use.
//
// A failure at any hop tears down what was already established, so a
// half-built chain never outlives the call that failed to finish it.
func establish(ctx context.Context, cfg *Config) (*chain, error) {
	c := &chain{}

	for _, spec := range hops(cfg) {
		client, agentConn, err := c.dial(ctx, spec)
		if err != nil {
			_ = c.Close()

			return nil, err
		}

		c.hops = append(c.hops, hop{client: client, agent: agentConn})
	}

	return c, nil
}

// dial establishes one hop: its transport, then its handshake, using that
// hop's own credentials and host-key policy. Nothing is inherited from the
// target — a jump host verifies against its own known_hosts entry and
// authenticates with its own key.
//
// It either returns a live client, whose agent socket the caller then owns,
// or releases everything it opened and reports why. The hop's own agent
// socket is opened before its transport, so every failure below has to
// release it.
func (c *chain) dial(ctx context.Context, spec hopSpec) (*ssh.Client, io.Closer, error) {
	cfg := spec.cfg

	auth, agentConn, err := authMethods(cfg)
	if err != nil {
		return nil, nil, spec.configFailure(err)
	}

	hostKeyCB, algorithms, err := resolveHostKey(cfg, spec.addr)
	if err != nil {
		closeAgent(agentConn)

		return nil, nil, spec.configFailure(err)
	}

	clientCfg := &ssh.ClientConfig{
		User:              loginUser(cfg.User),
		Auth:              auth,
		HostKeyCallback:   hostKeyCB,
		HostKeyAlgorithms: algorithms,
		Timeout:           cfg.timeout(),
	}

	// Every hop gets the timeout it was configured with, the way OpenSSH's
	// ConnectTimeout applies to each connection it makes; the caller's
	// context bounds the chain as a whole and can cut any hop shorter.
	dialCtx, cancel := context.WithTimeout(ctx, cfg.timeout())
	defer cancel()

	conn, err := c.transport(dialCtx, spec)
	if err != nil {
		closeAgent(agentConn)

		return nil, nil, spec.setupFailure(ctx, "dial", err)
	}

	client, err := c.handshake(dialCtx, conn, spec, clientCfg)
	if err != nil {
		closeAgent(agentConn)

		return nil, nil, spec.setupFailure(ctx, "handshake", err)
	}

	return client, agentConn, nil
}

// transport opens the connection this hop's handshake will run over: a
// socket from this process for the first hop, a channel forwarded by the
// previous hop for every one after it.
func (c *chain) transport(ctx context.Context, spec hopSpec) (net.Conn, error) {
	through := c.last()
	if through == nil {
		var dialer net.Dialer

		return dialer.DialContext(ctx, "tcp", spec.addr)
	}

	// The hop before this one opens a channel to the address and hands
	// back something that behaves like a socket. What rides on it is an
	// ordinary SSH connection, handshake and all.
	return through.DialContext(ctx, "tcp", spec.addr)
}

// handshake runs the SSH handshake over conn and returns the client for it.
//
// Two things bound it. A deadline on the connection covers a server that
// accepts and then says nothing — but only for the first hop, because the
// connection a jump host hands back is a channel, and a channel has no
// deadline to set. The watcher covers every hop, and covers plain
// cancellation for all of them, a cancellation having no deadline to fire.
func (c *chain) handshake(
	ctx context.Context,
	conn net.Conn,
	spec hopSpec,
	clientCfg *ssh.ClientConfig,
) (*ssh.Client, error) {
	_ = conn.SetDeadline(handshakeDeadline(ctx, spec.cfg.timeout()))

	handshakeDone := make(chan struct{})

	go func() {
		select {
		case <-ctx.Done():
			// A handshake that finished just as the deadline landed leaves
			// both arms ready and the choice between them arbitrary. It
			// must not be the one that tears down a connection that
			// succeeded.
			select {
			case <-handshakeDone:
			default:
				c.abort(conn)
			}
		case <-handshakeDone:
		}
	}()

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, spec.addr, clientCfg)

	close(handshakeDone)

	if err != nil {
		c.abort(conn)

		return nil, err
	}

	_ = conn.SetDeadline(time.Time{})

	return ssh.NewClient(sshConn, chans, reqs), nil
}

// handshakeDeadline is the earlier of the context's deadline and the
// configured timeout, so neither bound is exceeded.
func handshakeDeadline(ctx context.Context, timeout time.Duration) time.Time {
	deadline := time.Now().Add(timeout)

	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		return ctxDeadline
	}

	return deadline
}

// loginUser returns the configured user or the current OS user.
func loginUser(configured string) string {
	if configured != "" {
		return configured
	}

	if u, err := user.Current(); err == nil {
		return u.Username
	}

	return ""
}

// hopSpec is one hop's configuration together with what it takes to name
// that hop in an error.
type hopSpec struct {
	cfg *Config

	// addr is this hop's own host:port.
	addr string

	// jump marks a hop the connection passes through rather than the one
	// the caller asked for.
	jump bool

	// through is the address of the hop this one is reached through, and is
	// empty for the hop dialed from this process.
	through string
}

// hops orders a configured chain the way it is dialed. The configs point
// from the target back towards this process, each naming the hop it is
// reached through, so the walk that collects them is reversed.
func hops(cfg *Config) []hopSpec {
	var configs []*Config

	for hop := cfg; hop != nil; hop = hop.Jump {
		configs = append(configs, hop)
	}

	specs := make([]hopSpec, 0, len(configs))

	for i := len(configs) - 1; i >= 0; i-- {
		spec := hopSpec{
			cfg:  configs[i],
			addr: address(configs[i]),
			jump: i > 0,
		}

		if len(specs) > 0 {
			spec.through = specs[len(specs)-1].addr
		}

		specs = append(specs, spec)
	}

	return specs
}

// address is the hop's host and port, as both the dial and the host-key
// check need it.
func address(cfg *Config) string {
	return net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.port()))
}

// describe names a hop for an error message.
//
// A direct connection to the target is not named: there is only one
// connection the failure could be about, and the caller typed its address.
// Anything else is, because the address alone misleads — "connection
// refused" reported against the target reads as the target being down,
// when what happened is that the jump host would not open a channel to it.
func (h hopSpec) describe() string {
	if !h.jump && h.through == "" {
		return ""
	}

	name := h.addr
	if h.jump {
		name = "jump " + h.addr
	}

	if h.through != "" {
		name += " through jump " + h.through
	}

	return name
}

// setupFailure reports a hop's transport failing to come up.
//
// A context that has ended is reported as itself: the caller stopping the
// work is not the transport failing, and only one of those is worth
// retrying. This mirrors what the handshake has always done, and matters
// more now that a hop reached through another reports a bare cancellation
// with nothing else to identify it by.
func (h hopSpec) setupFailure(ctx context.Context, op string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("ssh: connect: %w", ctxErr)
	}

	// The hop is named inside the error rather than around it: the taxonomy
	// asks for one transport failure per failure, not a nest of them.
	if name := h.describe(); name != "" {
		err = fmt.Errorf("%s: %w", name, err)
	}

	return &invoke.TransportError{Op: op, Err: err}
}

// configFailure reports a hop's own misconfiguration — no usable
// authentication, no host-key verification, a key that will not load.
func (h hopSpec) configFailure(err error) error {
	name := h.describe()
	if name == "" {
		return err
	}

	return &hopError{hop: name, err: err}
}

// hopError names the connection a failure belongs to. The message it wraps
// already carries this package's prefix, and the name belongs directly
// after it, so a reader learns which connection is at fault without seeing
// the prefix twice.
type hopError struct {
	hop string
	err error
}

func (e *hopError) Error() string {
	return "ssh: " + e.hop + ": " + strings.TrimPrefix(e.err.Error(), "ssh: ")
}

func (e *hopError) Unwrap() error {
	return e.err
}
