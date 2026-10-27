// Package lifetime ties what an Environment runs to the Environment's own
// life, so Close can end it and wait for it.
//
// Two kinds of work run under an Environment. A process has a handle the
// caller holds: Start registers it with [Lifetime.Track], and Close ends
// every one still registered by closing it. A transfer has no handle, and
// it once watched only the caller's context, so Close returned while one
// ran on, finishing after it on some providers and failing as a retryable
// transport fault on another. An operation entered through [Lifetime.Run]
// runs on a context Close cancels, and Close does not return until every
// one of them has.
//
// The two have a race in common. Start checks that the Environment is
// open, then spends a while bringing the process up — several round trips,
// on a remote target — and a Close landing in that window has already
// gathered the processes it will end. Track re-checks under the same lock
// Close takes, so a process is either registered before Close gathers
// them, and ended with the rest, or refused, and torn down by its caller.
package lifetime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/ruffel/invoke"
)

// Lifetime is the life of one Environment. The zero value is not usable;
// call New.
type Lifetime struct {
	ctx    context.Context //nolint:containedctx // The Environment's own life, which every operation derives from.
	cancel context.CancelCauseFunc

	// mu guards closed and procs. Close is the only writer of closed;
	// Track and Untrack are the only writers of procs, and Track refuses
	// once closed is set.
	mu     sync.Mutex
	closed bool
	procs  map[io.Closer]struct{}

	// ops counts the operations in flight, which Close waits for; ended
	// closes once Close has finished, so every Close returns after the
	// end whichever call brought it about.
	ops   sync.WaitGroup
	ended chan struct{}
}

// New returns a Lifetime that has not ended.
func New() *Lifetime {
	ctx, cancel := context.WithCancelCause(context.Background())

	return &Lifetime{
		ctx:    ctx,
		cancel: cancel,
		procs:  make(map[io.Closer]struct{}),
		ended:  make(chan struct{}),
	}
}

// Err reports whether the Environment can still be used: nil while it is
// open, and invoke.ErrClosed once Close has begun.
func (l *Lifetime) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return invoke.ErrClosed
	}

	return nil
}

// Track registers a running process so Close ends it. Once Close has
// begun it refuses with invoke.ErrClosed, and the caller tears the
// process down itself: Close has already gathered the processes it will
// end, and one added afterwards would run with nothing left to stop it.
func (l *Lifetime) Track(p io.Closer) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return invoke.ErrClosed
	}

	l.procs[p] = struct{}{}

	return nil
}

// Untrack forgets a process whose outcome has settled.
func (l *Lifetime) Untrack(p io.Closer) {
	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.procs, p)
}

// Enter begins an operation, returning the context it runs on and the
// function that ends it, which the caller must call. The context ends with
// the caller's or when the Environment closes, whichever comes first. A
// closed Lifetime refuses with invoke.ErrClosed.
//
// [Lifetime.Run] is the usual form; Enter is for an operation that cannot
// be expressed as one function.
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

// Run runs op as one operation of the Environment, on a context that ends
// with the caller's or when the Environment closes. A closed Lifetime
// refuses with invoke.ErrClosed.
//
// An operation Close stopped reports that, wrapping invoke.ErrClosed,
// whatever op itself returned: Close stopped it, whether or not its last
// bytes had already been written, and a transfer cut short must not read
// as a success or as the retryable transport fault the cancellation
// otherwise produces.
func (l *Lifetime) Run(ctx context.Context, op func(context.Context) error) error {
	opCtx, end, err := l.Enter(ctx)
	if err != nil {
		return err
	}

	defer end()

	err = op(opCtx)

	if Ended(opCtx) {
		return fmt.Errorf("stopped by Close: %w", invoke.ErrClosed)
	}

	return err
}

// Close ends the life: new calls are refused, every operation in flight
// is canceled with invoke.ErrClosed as the cause and waited for, and
// every tracked process is closed. It is idempotent, and every call
// returns only once the life has ended, whichever call ended it.
func (l *Lifetime) Close() {
	_ = l.CloseWith(nil)
}

// CloseWith is Close followed by the provider's own teardown — a
// connection to drop, a probe loop to stop — which runs exactly once, in
// the call that ended the life, after everything that ran over that
// connection has stopped. Every call returns only once the teardown has
// finished too, so Close returning means the Environment is closed
// whichever call it was. The call that ran the teardown reports its
// error; the others report nil.
func (l *Lifetime) CloseWith(teardown func() error) error {
	l.mu.Lock()

	if l.closed {
		l.mu.Unlock()

		// A Close racing another still waits for everything the first
		// one stopped, and for its teardown: returning while either is
		// under way would let it run on past this call.
		<-l.ended

		return nil
	}

	l.closed = true

	procs := make([]io.Closer, 0, len(l.procs))
	for p := range l.procs {
		procs = append(procs, p)
	}

	l.mu.Unlock()

	// Released however the teardown ends, or a later Close waits forever.
	defer close(l.ended)

	l.cancel(invoke.ErrClosed)
	l.ops.Wait()

	for _, p := range procs {
		_ = p.Close()
	}

	if teardown == nil {
		return nil
	}

	return teardown()
}

// Ended reports whether ctx, from Enter, ended because the Environment
// closed rather than because the caller stopped.
func Ended(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), invoke.ErrClosed)
}
