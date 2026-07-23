//go:build unix

package local_test

import (
	"sync"
	"testing"
	"time"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/local"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// heldWriter reports its first write and then holds it until released,
// keeping the output copier inside the caller's writer for as long as
// the test needs.
type heldWriter struct {
	started     chan struct{}
	held        chan struct{}
	startOnce   sync.Once
	releaseOnce sync.Once
}

func newHeldWriter() *heldWriter {
	return &heldWriter{started: make(chan struct{}), held: make(chan struct{})}
}

func (w *heldWriter) Write(p []byte) (int, error) {
	w.startOnce.Do(func() { close(w.started) })

	<-w.held

	return len(p), nil
}

func (w *heldWriter) release() {
	w.releaseOnce.Do(func() { close(w.held) })
}

// TestTTYWaitJoinsTheCopier pins the boundary Wait draws under a
// terminal: once it returns, the caller's buffers are the caller's
// again. The grace period bounds how long an exited command's output is
// awaited — it must not bound the copier's last write. A Wait that
// returns while the copier is still inside the caller's writer leaves
// that write to land whenever the writer yields, racing whatever the
// caller does next with its own buffer. The non-terminal path already
// guarantees the join; this pins the terminal path to the same rule.
func TestTTYWaitJoinsTheCopier(t *testing.T) {
	t.Parallel()

	const (
		grace       = 100 * time.Millisecond
		observation = 600 * time.Millisecond
		bound       = 5 * time.Second
	)

	env, err := local.New(local.WithTerminationGrace(grace))
	require.NoError(t, err)

	t.Cleanup(func() { _ = env.Close() })

	writer := newHeldWriter()

	t.Cleanup(writer.release)

	proc, err := env.Start(t.Context(), invoke.Shell("echo held"),
		invoke.IO{TTY: &invoke.TTY{}, Stdout: writer})
	require.NoError(t, err)

	t.Cleanup(func() { _ = proc.Close() })

	select {
	case <-writer.started:
	case <-time.After(bound):
		t.Fatal("the command's output never reached the caller's writer")
	}

	waited := make(chan error, 1)

	go func() {
		_, waitErr := proc.Wait()
		waited <- waitErr
	}()

	// The command has exited and the grace has long expired; the copier
	// is still inside the caller's Write. Wait must be waiting with it.
	select {
	case <-waited:
		t.Fatal("Wait returned while the copier was still inside the caller's writer; " +
			"its write would land after the buffers went back to the caller")
	case <-time.After(observation):
	}

	writer.release()

	select {
	case waitErr := <-waited:
		assert.NoError(t, waitErr, "the command exited 0; the held drain must not rewrite that")
	case <-time.After(bound):
		t.Fatal("Wait did not return after the writer was released")
	}
}
