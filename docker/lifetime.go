package docker

// The core module carries the same mechanism as internal/lifetime. This
// module requires a released core, and none released carries it yet, so
// it is kept here until one does.
//
// It ties the operations an Environment runs to the Environment's own
// life, so Close can stop them and wait for them: a transfer has no
// handle the caller holds, and it watched only the caller's context.

import (
	"context"
	"errors"
	"sync"

	"github.com/ruffel/invoke"
)

// lifetime is the life of one Environment. The zero value is not usable;
// call newLifetime.
type lifetime struct {
	ctx    context.Context //nolint:containedctx // The Environment's own life, which every operation derives from.
	cancel context.CancelCauseFunc

	mu     sync.Mutex
	closed bool
	ops    sync.WaitGroup
}

// newLifetime returns a lifetime that has not ended.
func newLifetime() *lifetime {
	ctx, cancel := context.WithCancelCause(context.Background())

	return &lifetime{ctx: ctx, cancel: cancel}
}

// enter begins an operation, returning the context it runs on and the
// function that ends it, which the caller must call. The context ends with
// the caller's or when the Environment closes, whichever comes first. A
// closed Lifetime refuses with an error wrapping invoke.ErrClosed.
func (l *lifetime) enter(ctx context.Context) (context.Context, func(), error) {
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

// close cancels every operation in flight, with invoke.ErrClosed as the
// cause, and waits for them to return. It is idempotent.
func (l *lifetime) close() {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()

	l.cancel(invoke.ErrClosed)
	l.ops.Wait()
}

// lifetimeEnded reports whether ctx, from enter, ended because the Environment
// closed rather than because the caller stopped.
func lifetimeEnded(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), invoke.ErrClosed)
}
