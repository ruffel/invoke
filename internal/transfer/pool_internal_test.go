package transfer

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCopyPoolDropsWorkOnceCanceled pins the two ways a task can meet a
// canceled pool, both of which must end without the task running: one
// submitted while the workers are busy, which the pool must refuse
// rather than queue; and one a worker receives after the cancellation,
// which it must drain rather than start.
func TestCopyPoolDropsWorkOnceCanceled(t *testing.T) {
	t.Parallel()

	pool := newCopyPool(t.Context(), 1)

	started := make(chan struct{})
	release := make(chan struct{})

	pool.submit(func(context.Context) error {
		close(started)
		<-release

		return nil
	})

	<-started // The only worker is now busy.

	pool.cancel()

	var ran atomic.Bool

	late := func(context.Context) error {
		ran.Store(true)

		return nil
	}

	// Refused: the worker cannot receive, and the pool is canceled, so
	// submit must return rather than wait for a worker that would only
	// discard the task anyway.
	pool.submit(late)

	close(release)

	// Delivered anyway, bypassing submit's guard: what a task that won
	// the race against the cancellation looks like from the worker's
	// side.
	pool.tasks <- late

	require.NoError(t, pool.wait(), "no task failed; the pool must report nothing")
	assert.False(t, ran.Load(), "a task reaching a canceled pool must not run")
}
