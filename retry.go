package invoke

import (
	"context"
	"errors"
	"io"
	"os"
	"sync/atomic"
	"time"
)

// runWithRetry drives attempt up to the configured number of times,
// retrying only on TransportError and waiting per the backoff between
// attempts. The final TransportError is returned as-is, never re-wrapped.
func (e *Executor) runWithRetry(ctx context.Context, cfg execConfig, attempt func(attempt int) (Result, error)) (Result, error) {
	var (
		res Result
		err error
	)

	for n := 1; n <= cfg.attempts; n++ {
		if n > 1 {
			if werr := e.backoffWait(ctx, cfg, n); werr != nil {
				return res, werr
			}
		}

		res, err = attempt(n)
		if err == nil || !retryable(err) {
			return res, err
		}
	}

	return res, err
}

// retryTransfer runs a transfer under the executor's default retry policy.
// Transfer options are not execution options, so there is nothing to
// overlay per call.
func (e *Executor) retryTransfer(ctx context.Context, transfer func() error) error {
	if err := e.base.validate(); err != nil {
		return err
	}

	_, err := e.runWithRetry(ctx, e.base, func(int) (Result, error) {
		return Result{}, transfer()
	})

	return err
}

// backoffWait sleeps for the configured backoff before attempt n,
// returning the context error if it is canceled first.
func (e *Executor) backoffWait(ctx context.Context, cfg execConfig, n int) error {
	if cfg.backoff == nil {
		return ctx.Err()
	}

	delay := cfg.backoff(n)
	if delay <= 0 {
		return ctx.Err()
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// retryable reports whether err is a [TransportError] that carries no
// terminal outcome, as that type's doc sets out.
func retryable(err error) bool {
	var te *TransportError

	return errors.As(err, &te) && !terminal(err)
}

// terminal reports whether err carries an outcome that forecloses running
// the command again: it ran, or it could not run for a reason that asking
// again will not change.
//
// Cancellation is deliberately absent. A provider's own per-attempt
// deadline produces a context error inside a genuine transport failure,
// and treating that as terminal would strand a retry the caller wanted.
// The caller's own cancellation needs no help from here: the backoff
// before the next attempt returns the context's error, so the command is
// never started again regardless of how this reads.
func terminal(err error) bool {
	var (
		exitErr *ExitError
		refused *refusedOutputError
	)

	if errors.As(err, &exitErr) || errors.As(err, &refused) {
		return true
	}

	for _, sentinel := range []error{ErrClosed, ErrNotFound, ErrNotSupported, ErrInvalidWorkdir} {
		if errors.Is(err, sentinel) {
			return true
		}
	}

	return false
}

// refusalWatch notes whether any of an attempt's writers refused output.
//
// An attempt whose output the caller could not take is never retried. The
// command has run, and a provider reporting that the caller's writer
// failed passes on the writer's own error, which the executor cannot
// classify on the caller's behalf: a writer that is itself, say, an
// upload may well fail with a TransportError, though no transport to
// this command's target failed.
type refusalWatch struct {
	hit atomic.Bool
}

// wrap returns w reporting its failures here. A nil writer has nothing to
// refuse, and an *os.File is left alone: a provider may hand it to the
// command directly, and wrapping it would put a copy in the way.
func (r *refusalWatch) wrap(w io.Writer) io.Writer {
	if w == nil {
		return nil
	}

	if _, ok := w.(*os.File); ok {
		return w
	}

	return &watchedWriter{w: w, watch: r}
}

// refused reports whether a wrapped writer failed. A nil watch, for an
// attempt that cannot be retried, never did.
func (r *refusalWatch) refused() bool {
	return r != nil && r.hit.Load()
}

type watchedWriter struct {
	w     io.Writer
	watch *refusalWatch
}

func (w *watchedWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	if err != nil {
		w.watch.hit.Store(true)
	}

	return n, err
}

// refusedOutputError marks an attempt's error as terminal because the
// caller's writer failed during it. It adds nothing to the message and
// unwraps to the provider's error unchanged.
type refusedOutputError struct {
	err error
}

func (e *refusedOutputError) Error() string { return e.err.Error() }
func (e *refusedOutputError) Unwrap() error { return e.err }
