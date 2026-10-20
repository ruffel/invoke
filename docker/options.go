package docker

import (
	"time"
)

// defaultTimeout bounds daemon calls that must not block indefinitely,
// such as the kill issued when a command is canceled.
const defaultTimeout = 30 * time.Second

// Config holds the settings for executing in a container. Callers
// normally build it with [New] and the With options rather than by hand;
// [NewFromConfig] accepts one directly.
type Config struct {
	// Container is the container name or ID to execute in. Required.
	Container string

	// User is the user commands run as, in the same form docker accepts
	// ("root", "1000", "1000:1000"). Empty means the container's own
	// default.
	User string

	// Privileged runs commands with elevated privileges.
	Privileged bool

	// Host is the daemon endpoint, outranking every other source. Empty
	// means the endpoint the local docker installation is configured to
	// use, found in the order the package documentation gives: a context
	// named by Context or DOCKER_CONTEXT, then DOCKER_HOST, then the
	// current context's.
	Host string

	// Context names a docker context to take the endpoint from,
	// overriding whichever one is current — and DOCKER_HOST, as the
	// docker command's own --context flag does.
	Context string

	// Timeout bounds daemon calls that must not block indefinitely; zero
	// or negative means 30s. See [WithTimeout] for which calls those are.
	Timeout time.Duration
}

// Option configures a [Config].
type Option func(*Config)

// WithUser runs commands as the given user.
func WithUser(user string) Option {
	return func(c *Config) { c.User = user }
}

// WithPrivileged runs commands with elevated privileges.
func WithPrivileged() Option {
	return func(c *Config) { c.Privileged = true }
}

// WithHost sets the daemon endpoint, overriding both DOCKER_HOST and any
// context.
func WithHost(host string) Option {
	return func(c *Config) { c.Host = host }
}

// WithContext takes the daemon endpoint from the named docker context,
// rather than from whichever one the installation currently selects —
// overriding an ambient DOCKER_HOST too, as the docker command's own
// --context flag does.
//
// Naming a context that cannot be read fails, rather than falling back to
// the default endpoint: the fallback is a different daemon, and commands
// would run somewhere the caller did not choose.
func WithContext(name string) Option {
	return func(c *Config) { c.Context = name }
}

// WithTimeout bounds daemon calls that must not block indefinitely. Zero
// or negative means the default, 30s.
//
// It bounds each of the provider's own calls on its own: the container
// inspect and the probes [New] makes, the kill a Signal, Close or
// cancellation sends, the status asked for once a command's output ends,
// and the cleanup after a command or an upload. For Start's pre-flight
// check, LookPath and upload staging, this setting bounds only status
// polling after output ends; it does not cap exec creation, attachment
// or output collection.
//
// It is not a bound on a command or a transfer the caller asked for,
// which only their context and Close limit. Nor is it the ssh provider's
// WithTimeout, which bounds dialling and the handshake of a connection.
func WithTimeout(d time.Duration) Option {
	return func(c *Config) { c.Timeout = d }
}

// timeout returns the configured timeout or the default.
func (c *Config) timeout() time.Duration {
	if c.Timeout <= 0 {
		return defaultTimeout
	}

	return c.Timeout
}
