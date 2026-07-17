package invoke

import (
	"math/rand/v2"
	"time"
)

// execConfig holds the policy for one command invocation: retry, sudo
// wrapping, and per-attempt IO. It is assembled from the executor's
// defaults overlaid with per-call options.
type execConfig struct {
	attempts int
	backoff  BackoffFunc
	sudo     *sudoConfig
	freshIO  func(attempt int) IO
}

func defaultExecConfig() execConfig {
	return execConfig{attempts: 1}
}

// Option configures command execution policy. The same options apply both
// as executor defaults ([NewExecutor]) and as per-call overrides
// ([Executor.Run] and friends), with per-call options winning.
type Option func(*execConfig)

// BackoffFunc returns the delay to wait before a given retry attempt.
// attempt counts from 2 (the first retry); it is never called for the
// initial attempt.
type BackoffFunc func(attempt int) time.Duration

// WithRetry retries a command up to attempts times (a total count,
// including the first try) when it fails with a [TransportError] — the
// only retryable error family. backoff sets the delay between attempts and
// may be nil for no delay. attempts below 1 is a validation error at call
// time, never a silent no-op.
func WithRetry(attempts int, backoff BackoffFunc) Option {
	return func(c *execConfig) {
		c.attempts = attempts
		c.backoff = backoff
	}
}

// WithFreshIO supplies a fresh [IO] for each attempt, so a retried command
// gets un-consumed streams. It is required to retry a command whose IO
// carries a non-nil Stdin, since a consumed reader cannot be replayed.
func WithFreshIO(fn func(attempt int) IO) Option {
	return func(c *execConfig) {
		c.freshIO = fn
	}
}

// sudoConfig collects sudo wrapping options.
type sudoConfig struct {
	user        string
	group       string
	preserveEnv bool
	flags       []string
}

// SudoOption configures how [WithSudo] wraps a command.
type SudoOption func(*sudoConfig)

// WithSudo runs the command through sudo in non-interactive mode
// (sudo -n), so a password prompt fails rather than hangs.
//
// The command and its arguments are passed after a -- separator as an
// argv, never as a shell string, so no argument can be misread as a sudo
// flag or interpreted by a shell. Note that sudo resets the environment by
// default: variables set via Command.Env do not reach the target command
// unless the sudoers policy preserves them or [WithSudoPreserveEnv] is set
// and permitted.
func WithSudo(opts ...SudoOption) Option {
	return func(c *execConfig) {
		sc := &sudoConfig{}
		for _, opt := range opts {
			opt(sc)
		}

		c.sudo = sc
	}
}

// WithSudoUser runs the command as the given target user (sudo -u).
func WithSudoUser(user string) SudoOption {
	return func(c *sudoConfig) {
		c.user = user
	}
}

// WithSudoGroup runs the command as the given target group (sudo -g).
func WithSudoGroup(group string) SudoOption {
	return func(c *sudoConfig) {
		c.group = group
	}
}

// WithSudoPreserveEnv preserves the invoking environment (sudo -E), subject
// to the sudoers policy allowing it.
func WithSudoPreserveEnv() SudoOption {
	return func(c *sudoConfig) {
		c.preserveEnv = true
	}
}

// WithSudoFlags appends additional flags to the sudo invocation, before the
// -- separator. Callers own the safety of anything passed here.
func WithSudoFlags(flags ...string) SudoOption {
	return func(c *sudoConfig) {
		c.flags = append(c.flags, flags...)
	}
}

// ConstantBackoff waits a fixed delay before every retry.
func ConstantBackoff(d time.Duration) BackoffFunc {
	return func(int) time.Duration {
		return d
	}
}

// ExponentialBackoff waits base before the first retry and doubles the
// delay for each subsequent one.
func ExponentialBackoff(base time.Duration) BackoffFunc {
	// maxShift caps growth so the doubling cannot overflow the delay.
	const maxShift = 16

	return func(attempt int) time.Duration {
		shift := max(0, min(attempt-2, maxShift))

		return base << shift
	}
}

// WithJitter wraps a backoff so each delay is randomly reduced by up to
// fraction of its value (0 < fraction <= 1), spreading retries to avoid a
// thundering herd.
func WithJitter(backoff BackoffFunc, fraction float64) BackoffFunc {
	return func(attempt int) time.Duration {
		base := backoff(attempt)
		if base <= 0 || fraction <= 0 {
			return base
		}

		spread := int64(float64(base) * fraction)
		if spread <= 0 {
			return base
		}

		return base - time.Duration(rand.Int64N(spread)) //nolint:gosec // Jitter timing, not security.
	}
}
