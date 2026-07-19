package local

import "time"

// defaultTerminationGrace is how long a terminated command is given to
// finish writing before it is abandoned, when none is configured.
const defaultTerminationGrace = 2 * time.Second

// config holds the settings for a local [Environment]. Callers set it
// through [New] and the With options rather than directly.
type config struct {
	terminationGrace time.Duration
}

// Option configures a local [Environment].
type Option func(*config)

// WithTerminationGrace sets how long a command that has been canceled or
// closed is given to finish writing before it is abandoned.
//
// Terminating a command does not necessarily close its output: a
// descendant it left behind can hold the pipes open, and waiting on them
// forever would hang the caller. Once the grace period passes the command
// is abandoned and Wait returns, which is why the default is short.
//
// A command that flushes something substantial when told to stop needs
// longer than the default two seconds, or its last output is lost. A
// caller that would rather never wait can set a shorter one.
func WithTerminationGrace(d time.Duration) Option {
	return func(c *config) { c.terminationGrace = d }
}

// grace returns the configured termination grace period or the default.
func (c *config) grace() time.Duration {
	if c.terminationGrace <= 0 {
		return defaultTerminationGrace
	}

	return c.terminationGrace
}
