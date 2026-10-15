// Package lifetime ties the operations an Environment runs to the
// Environment's own life, so Close can stop them and wait for them.
//
// A process is tracked by the provider and ended by name. A transfer has
// no handle the caller holds, and it watched only the caller's context:
// Close returned while one ran on, finishing after it on some providers
// and failing as a retryable transport fault on another. An operation
// entered through a Lifetime runs on a context Close cancels, and Close
// does not return until every one of them has.
package lifetime

import (
	"context"
	"errors"
	"sync"

	"github.com/ruffel/invoke"
)

// Lifetime is the life of one Environment. The zero value is not usable;
// call New.
type Lifetime struct {
	ctx    context.Context //nolint:containedctx // The Environment's own life, which every operation derives from.
	cancel context.CancelCauseFunc

	mu     sync.Mutex
	closed bool
	ops    sync.WaitGroup
}

// New returns a Lifetime that has not ended.
func New() *Lifetime {
	ctx, cancel := context.WithCancelCause(context.Background())

	return &Lifetime{ctx: ctx, cancel: cancel}
}

// Enter begins an operation, returning the context it runs on and the
// function that ends it, which the caller must call. The context ends with
// the caller's or when the Environment closes, whichever comes first. A
// closed Lifetime refuses with an error wrapping invoke.ErrClosed.
func (l *Lifetime) Enter(ctx context.Context) (context.Context, func(), error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return nil, nil, invoke.ErrClosed
	}

	l.ops.Add(1)

	opCtx, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(l.ctx, func() { cancel(context.Cause(l.ctx)) })

	return opCtx, func() {
		stop()
		cancel(nil)
		l.ops.Done()
	}, nil
}

// Close cancels every operation in flight, with invoke.ErrClosed as the
// cause, and waits for them to return. It is idempotent.
func (l *Lifetime) Close() {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()

	l.cancel(invoke.ErrClosed)
	l.ops.Wait()
}

// Ended reports whether ctx, from Enter, ended because the Environment
// closed rather than because the caller stopped.
func Ended(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), invoke.ErrClosed)
}
