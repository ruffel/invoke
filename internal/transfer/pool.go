package transfer

import (
	"context"
	"errors"
	"sync"

	"github.com/ruffel/invoke"
)

// ConcurrencyHinter is implemented by an FS that benefits from copying
// several files at once — a request-response transport whose per-file
// cost is round trips. CopyConcurrency is the preferred worker count
// for tree copies touching this side.
type ConcurrencyHinter interface {
	CopyConcurrency() int
}

// treeWorkers resolves how many files a tree copy runs at once: the
// caller's explicit choice when one was made, and otherwise the larger
// preference of the two sides — so a transfer overlaps round trips when
// either endpoint is bound by them, and stays sequential when neither
// is.
func treeWorkers(cfg invoke.TransferConfig, src, dst FS) int {
	if cfg.Concurrency >= 1 {
		return cfg.Concurrency
	}

	workers := 1

	for _, side := range []FS{src, dst} {
		if hinter, ok := side.(ConcurrencyHinter); ok && hinter.CopyConcurrency() > workers {
			workers = hinter.CopyConcurrency()
		}
	}

	return workers
}

// copyPool runs file copies on a bounded set of workers, canceling the
// walk and every in-flight copy after the first failure.
type copyPool struct {
	ctx    context.Context //nolint:containedctx // The pool is one tree copy's scope, not a stored object.
	cancel context.CancelFunc
	tasks  chan func(context.Context) error
	wg     sync.WaitGroup

	mu    sync.Mutex
	first error
}

// newCopyPool starts workers goroutines serving submitted copies under
// a context the first failure cancels.
func newCopyPool(ctx context.Context, workers int) *copyPool {
	poolCtx, cancel := context.WithCancel(ctx)

	p := &copyPool{
		ctx:    poolCtx,
		cancel: cancel,
		tasks:  make(chan func(context.Context) error),
	}

	p.wg.Add(workers)

	for range workers {
		go func() {
			defer p.wg.Done()

			for task := range p.tasks {
				// A copy already failed: drain without running rather
				// than starting work whose transfer is decided.
				if p.ctx.Err() != nil {
					continue
				}

				if err := task(p.ctx); err != nil {
					p.fail(err)
				}
			}
		}()
	}

	return p
}

// fail cancels the copy and records err as the transfer's outcome —
// unless err is only the cancellation's own echo. A copy interrupted
// because something else failed reports the context error it was
// stopped with, and recording that would let an echo outrace the cause
// it echoes.
func (p *copyPool) fail(err error) {
	p.cancel()

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.first == nil {
		p.first = err
	}
}

// submit queues one file copy. It blocks while every worker is busy —
// the backpressure that keeps the walk from racing ahead of the copies —
// and drops the task once the copy is canceled, where running it could
// only fail.
func (p *copyPool) submit(task func(context.Context) error) {
	select {
	case p.tasks <- task:
	case <-p.ctx.Done():
	}
}

// wait drains the pool and returns the first recorded failure, if any.
func (p *copyPool) wait() error {
	close(p.tasks)
	p.wg.Wait()
	p.cancel()

	p.mu.Lock()
	defer p.mu.Unlock()

	return p.first
}
