package ssh

import (
	"testing"
	"time"

	"github.com/ruffel/invoke/internal/transfer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSFTPSideAdvertisesConcurrency pins the default that lets tree
// transfers overlap their per-file round trips without the caller
// asking. Losing it would break nothing visible — every transfer would
// still deliver — it would just quietly cost the tree speedup, which is
// exactly the kind of regression a test has to name.
func TestSFTPSideAdvertisesConcurrency(t *testing.T) {
	t.Parallel()

	var hinter transfer.ConcurrencyHinter = sftpFS{}

	assert.Greater(t, hinter.CopyConcurrency(), 1,
		"the SFTP side must prefer copying several files at once; its per-file cost is round trips")
}

// TestCleanupIsBoundedBySilenceNotElapsedTime pins what ends the wait for
// a canceled transfer to unwind. Removing the temp file it was writing
// costs round trips, and how many depends on the link and on how many
// files were in flight; a budget on the total would cut a slow link off
// mid-cleanup and leave exactly the files this waits for. Only silence
// ends it.
func TestCleanupIsBoundedBySilenceNotElapsedTime(t *testing.T) {
	t.Parallel()

	const (
		idle   = 40 * time.Millisecond
		checks = 5
	)

	done := make(chan transferResult, 1)

	// The server answers steadily, as a slow link's close-and-remove
	// round trips do, for longer than the grace; then the copy finishes.
	answered := 0
	idleFor := func() time.Duration {
		answered++
		if answered >= checks {
			done <- transferResult{}
		}

		return idle / 2
	}

	start := time.Now()

	assert.False(t, wentSilent(done, idleFor, idle),
		"a session that is still answering must not be torn down under the copy cleaning up over it")

	assert.Greater(t, time.Since(start), idle,
		"the wait must have outlasted the grace, or this proves nothing about bounding on silence")
}

// TestASilentSessionIsTornDown is the other half: a server that has
// stopped answering cannot be waited out, and the stalled round trip ends
// only when the session goes.
func TestASilentSessionIsTornDown(t *testing.T) {
	t.Parallel()

	never := make(chan transferResult)
	silent := func() time.Duration { return time.Hour }

	assert.True(t, wentSilent(never, silent, 40*time.Millisecond),
		"a session that has stopped answering must be torn down")
}

// TestUnwindDoesNotWaitBeforeTheCopyHasASession pins the setup case: with
// no client there is no copy holding the session and no temp file to
// remove, so a cancellation during the subsystem handshake tears down at
// once rather than waiting out a grace that cannot change anything.
func TestUnwindDoesNotWaitBeforeTheCopyHasASession(t *testing.T) {
	t.Parallel()

	var session sftpSession

	done := make(chan transferResult, 1)
	done <- transferResult{}

	finished := make(chan struct{})

	go func() {
		session.unwind(done, time.Hour)
		close(finished)
	}()

	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		require.Fail(t, "unwind waited out a grace on a session the copy never had")
	}
}
