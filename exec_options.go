package invoke

import (
	"fmt"
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

// validate rejects a policy no attempt could run under. It is checked when
// a call is made rather than when an option is applied: an option has no
// error to return, and a bad value is never a silent no-op.
func (c execConfig) validate() error {
	if c.attempts < 1 {
		return fmt.Errorf("invoke: retry attempts must be at least 1, got %d", c.attempts)
	}

	return nil
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

// WithFreshIO supplies a fresh [IO] for each attempt, so a retried command
// gets un-consumed streams. It is required to retry a command whose IO
// carries a non-nil Stdin, since a consumed reader cannot be replayed.
// The capture helpers ([Executor.Output], [Executor.Text]) own their
// streams and override any WithFreshIO passed to them.
func WithFreshIO(fn func(attempt int) IO) Option {
	return func(c *execConfig) {
		c.freshIO = fn
	}
}
