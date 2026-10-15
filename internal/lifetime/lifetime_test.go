package lifetime_test

import (
	"context"
	"testing"
	"time"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/internal/lifetime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCloseCancelsAndWaitsForOperations(t *testing.T) {
	t.Parallel()

	life := lifetime.New()

	ctx, end, err := life.Enter(t.Context())
	require.NoError(t, err)

	returned := make(chan struct{})

	go func() {
		<-ctx.Done()
		time.Sleep(50 * time.Millisecond) // the operation unwinding

		// Signaled before end, which is what releases Close.
		close(returned)
		end()
	}()

	life.Close()

	select {
	case <-returned:
	default:
		t.Fatal("Close returned before the operation it canceled did")
	}

	assert.True(t, lifetime.Ended(ctx), "the operation ended because the Environment closed")
}

func TestEnterAfterCloseRefuses(t *testing.T) {
	t.Parallel()

	life := lifetime.New()
	life.Close()
	life.Close()

	_, _, err := life.Enter(t.Context())
	require.ErrorIs(t, err, invoke.ErrClosed)
}

func TestTheCallersCancellationIsNotAClose(t *testing.T) {
	t.Parallel()

	life := lifetime.New()

	parent, cancel := context.WithCancel(t.Context())

	ctx, end, err := life.Enter(parent)
	require.NoError(t, err)

	defer end()

	cancel()
	<-ctx.Done()

	assert.False(t, lifetime.Ended(ctx), "the caller stopped it; the Environment is still open")
	require.ErrorIs(t, ctx.Err(), context.Canceled)
}
