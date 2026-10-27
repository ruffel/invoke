package lifetime_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/internal/lifetime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// closer records whether Close reached it.
type closer struct {
	closed chan struct{}
}

func newCloser() *closer {
	return &closer{closed: make(chan struct{})}
}

func (c *closer) Close() error {
	close(c.closed)

	return nil
}

func (c *closer) wasClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

func TestCloseCancelsAndWaitsForOperations(t *testing.T) {
	t.Parallel()

	life := lifetime.New()

	started := make(chan struct{})
	returned := make(chan struct{})

	go func() {
		_ = life.Run(t.Context(), func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			time.Sleep(50 * time.Millisecond) // the operation unwinding

			// Signaled before the operation returns, which is what
			// releases Close.
			close(returned)

			return nil
		})
	}()

	<-started

	life.Close()

	select {
	case <-returned:
	default:
		t.Fatal("Close returned before the operation it canceled did")
	}
}

func TestRunReportsAnOperationCloseStopped(t *testing.T) {
	t.Parallel()

	life := lifetime.New()

	started := make(chan struct{})
	result := make(chan error, 1)

	go func() {
		result <- life.Run(t.Context(), func(ctx context.Context) error {
			close(started)
			<-ctx.Done()

			// The operation finished regardless: Close stopped it all
			// the same, and that is what the caller must hear.
			return nil
		})
	}()

	<-started
	life.Close()

	err := <-result
	require.ErrorIs(t, err, invoke.ErrClosed, "an operation Close stopped reports it, whatever it returned")
}

func TestRunAndEnterRefuseAfterClose(t *testing.T) {
	t.Parallel()

	life := lifetime.New()
	life.Close()
	life.Close()

	require.ErrorIs(t, life.Err(), invoke.ErrClosed)

	err := life.Run(t.Context(), func(context.Context) error {
		t.Error("an operation must not run in a closed Environment")

		return nil
	})
	require.ErrorIs(t, err, invoke.ErrClosed)

	_, _, err = life.Enter(t.Context())
	require.ErrorIs(t, err, invoke.ErrClosed)
}

func TestTheCallersCancellationIsNotAClose(t *testing.T) {
	t.Parallel()

	life := lifetime.New()

	parent, cancel := context.WithCancel(t.Context())
	cancel()

	err := life.Run(parent, func(ctx context.Context) error {
		<-ctx.Done()

		assert.False(t, lifetime.Ended(ctx), "the caller stopped it; the Environment is still open")

		return ctx.Err()
	})

	require.ErrorIs(t, err, context.Canceled)
	require.NotErrorIs(t, err, invoke.ErrClosed)
	require.NoError(t, life.Err(), "the Environment is still open")
}

// TestTrackRefusesAProcessAfterClose pins the guard that keeps a process
// from being registered into a closed Environment: Close has already
// gathered the processes it will end, so one added afterwards would run
// with nothing left to stop it.
func TestTrackRefusesAProcessAfterClose(t *testing.T) {
	t.Parallel()

	life := lifetime.New()

	tracked := newCloser()
	require.NoError(t, life.Track(tracked), "an open Environment must accept a process")

	settled := newCloser()
	require.NoError(t, life.Track(settled))
	life.Untrack(settled)

	life.Close()

	assert.True(t, tracked.wasClosed(), "a tracked process is ended by Close")
	assert.False(t, settled.wasClosed(), "a process that settled before Close is left alone")

	late := newCloser()

	err := life.Track(late)
	require.ErrorIs(t, err, invoke.ErrClosed, "Track into a closed Environment must refuse with ErrClosed")
	assert.False(t, late.wasClosed(), "a refused process is its caller's to tear down")
}

// TestASecondCloseWaitsForTheFirst pins that a Close never returns while
// the life is still ending under another call: neither while the
// operations the first Close stopped unwind, nor while its teardown runs.
func TestASecondCloseWaitsForTheFirst(t *testing.T) {
	t.Parallel()

	life := lifetime.New()

	started := make(chan struct{})
	release := make(chan struct{})
	releaseTeardown := make(chan struct{})

	go func() {
		_ = life.Run(t.Context(), func(context.Context) error {
			close(started)
			<-release

			return nil
		})
	}()

	<-started

	errTeardown := errors.New("the connection would not close")

	first := make(chan error, 1)
	second := make(chan error, 1)

	go func() {
		first <- life.CloseWith(func() error {
			<-releaseTeardown

			return errTeardown
		})
	}()

	// The second Close must find the life closing, not open.
	require.Eventually(t, func() bool { return errors.Is(life.Err(), invoke.ErrClosed) },
		time.Second, time.Millisecond)

	go func() {
		second <- life.CloseWith(func() error {
			t.Error("the teardown ran a second time")

			return nil
		})
	}()

	notYet := func(why string) {
		t.Helper()

		select {
		case <-second:
			t.Fatal("a second Close returned " + why)
		case <-time.After(50 * time.Millisecond):
		}
	}

	notYet("while the operation the first stopped was still running")

	close(release)

	notYet("while the first's teardown was still running")

	close(releaseTeardown)

	require.ErrorIs(t, <-first, errTeardown, "the call that ended the life reports its teardown's error")
	require.NoError(t, <-second, "a later Close finds the life ended")
}
