package ssh

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// defaultPort is the standard SSH port, used when none is configured.
const defaultPort = 22

// defaultTimeout bounds connection establishment (TCP dial plus SSH
// handshake) when none is configured.
const defaultTimeout = 30 * time.Second

// defaultKeepAlive is how often the connection is probed when no interval
// is configured.
const defaultKeepAlive = 30 * time.Second

// Config holds the settings for connecting to an SSH target. Callers
// normally build it with [New] and the With options rather than by hand;
// [NewFromConfig] accepts one directly.
type Config struct {
	// Host is the target hostname or address. Required.
	Host string

	// Port is the target port; zero means 22.
	Port int

	// User is the login user; empty means the current OS user.
	User string

	// Password enables password authentication when non-empty.
	Password string

	// PrivateKeyPath is a private key file for public-key authentication.
	PrivateKeyPath string

	// PrivateKeyPassphrase decrypts an encrypted PrivateKeyPath.
	PrivateKeyPassphrase string

	// UseAgent enables authentication via the SSH agent at SSH_AUTH_SOCK.
	UseAgent bool

	// HostKeyCallback verifies the server's host key. It is required:
	// [New] fails closed if none is provided and no known_hosts source
	// is configured. Use [WithKnownHosts], [WithHostKeyCallback], or —
	// for tests only — [WithInsecureIgnoreHostKey].
	HostKeyCallback ssh.HostKeyCallback

	// knownHostsPath is a known_hosts file used to build both the host-key
	// callback and the negotiated host-key algorithms.
	knownHostsPath string

	// insecureHostKey disables host-key verification entirely. Tests only.
	insecureHostKey bool

	// Timeout bounds establishing this connection — its dial and its
	// handshake — and zero means 30s. Each hop of a chain carries its own.
	Timeout time.Duration

	// KeepAlive is how often to probe the server so a silently dropped
	// connection is discovered. Zero means 30s; negative disables it.
	KeepAlive time.Duration

	// CommandLineEnv allows environment variables the server refuses to
	// accept out of band to be carried on the command line instead. See
	// [WithCommandLineEnv] for what that exposes.
	CommandLineEnv bool

	// Jump, when set, is the host this connection is dialed through: a
	// connection of its own, with its own credentials and host-key policy.
	// A hop carrying a Jump of its own extends the chain, which is dialed
	// from the hop this process can reach inwards to the target.
	//
	// Prefer [WithJumpHost] to setting this by hand. A hop assembled as a
	// struct cannot express known_hosts verification, whose fields are
	// unexported — but options are ordinary functions and can be applied
	// to one directly:
	//
	//	cfg.Jump = &ssh.Config{Host: "203.0.113.11"}
	//	ssh.WithUser("jump")(cfg.Jump)
	//	ssh.WithKnownHosts(path)(cfg.Jump)
	Jump *Config
}

// validate checks the whole chain before anything is dialed, so a
// misconfigured hop is named up front rather than surfacing later as a
// failure against an address the caller cannot place.
func (c *Config) validate() error {
	seen := make(map[*Config]struct{})

	for hop := c; hop != nil; hop = hop.Jump {
		if _, ok := seen[hop]; ok {
			return errors.New("ssh: the jump chain loops back on itself")
		}

		seen[hop] = struct{}{}

		if err := hop.validateHost(hop == c); err != nil {
			return err
		}
	}

	return nil
}

// validateHost checks one hop names a host that can be dialed.
func (c *Config) validateHost(target bool) error {
	subject := "jump host"
	if target {
		subject = "host"
	}

	if strings.TrimSpace(c.Host) == "" {
		return fmt.Errorf("ssh: %s is required", subject)
	}

	// OpenSSH's ProxyJump takes user@host:port in one string. This API
	// takes the host alone, with the rest as options, so a value pasted
	// from ssh_config would otherwise be dialed as a hostname with an @ in
	// it and fail to resolve for a reason nothing explains.
	if strings.Contains(c.Host, "@") {
		return fmt.Errorf("ssh: %s %q must not carry a user; set the login user with WithUser", subject, c.Host)
	}

	return nil
}

// Option configures a [Config].
type Option func(*Config)

// WithPort sets the target port.
func WithPort(port int) Option {
	return func(c *Config) { c.Port = port }
}

// WithUser sets the login user.
func WithUser(user string) Option {
	return func(c *Config) { c.User = user }
}

// WithPassword enables password authentication.
func WithPassword(password string) Option {
	return func(c *Config) { c.Password = password }
}

// WithPrivateKey enables public-key authentication using the key at path.
func WithPrivateKey(path string) Option {
	return func(c *Config) { c.PrivateKeyPath = path }
}

// WithPrivateKeyPassphrase supplies the passphrase for an encrypted key.
func WithPrivateKeyPassphrase(passphrase string) Option {
	return func(c *Config) { c.PrivateKeyPassphrase = passphrase }
}

// WithAgent enables authentication via the running SSH agent.
func WithAgent() Option {
	return func(c *Config) { c.UseAgent = true }
}

// WithHostKeyCallback sets the host-key verification callback directly.
func WithHostKeyCallback(cb ssh.HostKeyCallback) Option {
	return func(c *Config) { c.HostKeyCallback = cb }
}

// WithKnownHosts verifies the server against the given known_hosts file,
// and constrains host-key negotiation to the algorithms recorded there for
// this host so a host known under one key type is not rejected when the
// server offers another.
func WithKnownHosts(path string) Option {
	return func(c *Config) { c.knownHostsPath = path }
}

// WithInsecureIgnoreHostKey disables host-key verification. It is unsafe
// against man-in-the-middle attacks and intended only for tests.
func WithInsecureIgnoreHostKey() Option {
	return func(c *Config) { c.insecureHostKey = true }
}

// WithTimeout sets the connection establishment timeout: the dial and the
// handshake of one connection, as OpenSSH's ConnectTimeout bounds one.
// Where [WithJumpHost] builds a chain, each hop is bounded by the timeout
// it was configured with, and the caller's context bounds the chain as a
// whole.
func WithTimeout(d time.Duration) Option {
	return func(c *Config) { c.Timeout = d }
}

// WithCommandLineEnv allows environment variables to be carried on the
// remote command line when the server refuses to accept them out of band.
//
// A server accepts only the variables its AcceptEnv setting names, and
// the stock setting names none. Refused variables ordinarily travel in a
// file only the login user can read, which the command line sources and
// deletes before the command runs; that route needs a writable /tmp, and
// reserves exit status 93 to report a file that could not be read. With
// this option the refused variables are exported on the command line
// instead — where they appear in the remote process table and every
// account on the host can read them. Do not use it to pass secrets.
//
// It applies to the target alone. Set on a hop of a chain it does nothing,
// there being no commands to carry anything for.
func WithCommandLineEnv() Option {
	return func(c *Config) { c.CommandLineEnv = true }
}

// WithKeepAlive sets how often the connection is probed so a silently
// dropped link is discovered. Probing is also the discovery bound: a
// probe unanswered within the interval — floored at one second, since a
// tighter bound measures scheduling noise rather than the link —
// declares the connection dead and closes it, unblocking everything
// still waiting on it, including Close. Zero means the default; a
// negative interval disables probing.
//
// One probe covers a whole chain: it travels to the target over every hop,
// so a jump host that dies takes the probe with it and is discovered on
// the same interval. Setting this on a hop is therefore unnecessary, and
// does nothing.
func WithKeepAlive(d time.Duration) Option {
	return func(c *Config) { c.KeepAlive = d }
}

// WithJumpHost routes the connection through an intermediate SSH host, as
// OpenSSH's ProxyJump directive and its -J flag do. The jump host is dialed
// and authenticated first, and the target is reached through it: the
// target's handshake is unchanged, only what carries it differs.
//
// The jump connection is configured independently of the target, from the
// same options: its own user, credentials, port, and host-key verification.
// Nothing is inherited in either direction, and fail-closed host-key
// verification applies to each hop separately —
// [WithInsecureIgnoreHostKey] on the target does not extend to the jump.
//
//	env, err := ssh.New(ctx, "10.0.17.11",
//	    ssh.WithUser("root"),
//	    ssh.WithPrivateKey(keyPath),
//	    ssh.WithKnownHosts(knownHosts),
//	    ssh.WithJumpHost("203.0.113.11",
//	        ssh.WithUser("jump"),
//	        ssh.WithPrivateKey(jumpKeyPath),
//	        ssh.WithKnownHosts(knownHosts),
//	    ),
//	)
//
// Longer chains are written as repeated options, in the order the hops are
// dialed, so that
//
//	ssh.New(ctx, target, ssh.WithJumpHost("a"), ssh.WithJumpHost("b"))
//
// means what ssh -J a,b target means: a is reached from here, b through a,
// and the target through b. A hop may equally carry a jump host of its own,
// which says the same thing nested — WithJumpHost("b", WithJumpHost("a"))
// is that chain.
//
// The host is bare: a user belongs in [WithUser] and a port in [WithPort],
// not in a user@host:port string. Two options do nothing here.
// [WithCommandLineEnv] has nothing to act on, a hop running no commands.
// [WithKeepAlive] is not needed: probes to the target travel over every
// hop, so a jump host that dies silently is discovered with it.
//
// This package never reads ssh_config. A ProxyJump recorded there has no
// effect on a connection made here; the chain is the one these options
// describe.
func WithJumpHost(host string, opts ...Option) Option {
	return func(c *Config) {
		// Built when the option is applied, not when it is created: an
		// option is a value a caller may keep and use for several
		// connections, and a hop built once would be shared between all of
		// them.
		jump := &Config{Host: host}

		for _, opt := range opts {
			opt(jump)
		}

		// Hops accumulate in the order they are named, which is the order
		// they are dialed. Whatever this connection already reaches
		// through is therefore reached before this hop, and belongs at the
		// outer end of the hop's own chain.
		outermost := jump
		for outermost.Jump != nil {
			outermost = outermost.Jump
		}

		outermost.Jump = c.Jump

		c.Jump = jump
	}
}

// keepAlive returns the configured keepalive interval or the default. A
// negative value disables probing.
func (c *Config) keepAlive() time.Duration {
	if c.KeepAlive == 0 {
		return defaultKeepAlive
	}

	return c.KeepAlive
}

// port returns the configured port or the default.
func (c *Config) port() int {
	if c.Port == 0 {
		return defaultPort
	}

	return c.Port
}

// timeout returns the configured timeout or the default.
func (c *Config) timeout() time.Duration {
	if c.Timeout <= 0 {
		return defaultTimeout
	}

	return c.Timeout
}
