package ssh

import (
	"time"

	"golang.org/x/crypto/ssh"
)

// defaultPort is the standard SSH port, used when none is configured.
const defaultPort = 22

// defaultTimeout bounds connection establishment (TCP dial plus SSH
// handshake) when none is configured.
const defaultTimeout = 30 * time.Second

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

	// Timeout bounds connection establishment; zero means 30s.
	Timeout time.Duration
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

// WithTimeout sets the connection establishment timeout.
func WithTimeout(d time.Duration) Option {
	return func(c *Config) { c.Timeout = d }
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
