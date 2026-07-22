package docker

import (
	"testing"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTrackRefusesAProcessAfterClose pins the guard that keeps a process
// from being registered into a closed environment.
//
// Start checks the closed flag at entry and then creates the exec over
// several daemon round-trips. A Close in that window has already gathered
// the processes it will terminate, so one added afterwards would run with
// nothing left to stop it. track must refuse it instead, and leave the
// active set untouched, so Start knows to tear the exec down.
func TestTrackRefusesAProcessAfterClose(t *testing.T) {
	t.Parallel()

	env := &Environment{active: make(map[*process]struct{})}

	p := &process{}

	require.NoError(t, env.track(p), "an open environment must accept a process")

	_, registered := env.active[p]
	assert.True(t, registered, "an accepted process must be registered for termination")

	// The environment closes while a second Start is between its own entry
	// check and this point.
	env.mu.Lock()
	env.closed = true
	env.mu.Unlock()

	other := &process{}

	err := env.track(other)
	require.ErrorIs(t, err, invoke.ErrClosed, "track into a closed environment must refuse with ErrClosed")

	_, leaked := env.active[other]
	assert.False(t, leaked,
		"a refused process must not be registered, or Close will already have passed it by")
}
