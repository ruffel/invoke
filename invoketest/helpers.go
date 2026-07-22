package invoketest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// contractTimeout bounds every blocking step inside a contract, so a
// provider that hangs produces a failed contract rather than a hung suite.
const contractTimeout = 5 * time.Second

// token returns a short random hex string for unique target-side paths.
func token(t T) string {
	t.Helper()

	var raw [8]byte

	_, err := rand.Read(raw[:])
	require.NoError(t, err, "generating random token")

	return hex.EncodeToString(raw[:])
}

// exitSettle is how long a contract waits for a process to finish exiting
// once its last output has been observed. It is a margin around an event
// already witnessed rather than a substitute for witnessing one: the
// output says the command reached its final act, and this covers the
// short walk from there to the process being gone.
const exitSettle = 250 * time.Millisecond

// blockingWriter is a destination for a process's output that reports the
// first write and then holds it, so a contract can keep a provider inside
// its own drain for as long as it needs to.
type blockingWriter struct {
	// started closes once output has arrived, so a contract can wait for
	// the process to have written rather than guess when it did.
	started chan struct{}

	// held blocks the write until the contract releases it.
	held chan struct{}

	once sync.Once
}

func newBlockingWriter() *blockingWriter {
	return &blockingWriter{
		started: make(chan struct{}),
		held:    make(chan struct{}),
	}
}

// Write reports the first write, then blocks until the writer is released.
func (w *blockingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })

	<-w.held

	return len(p), nil
}

// release unblocks the write, letting the provider finish draining.
func (w *blockingWriter) release() {
	close(w.held)
}

// startCommand starts cmd and fails the contract on error.
func startCommand(ctx context.Context, t T, env invoke.Environment, cmd invoke.Command, stdio invoke.IO) invoke.Process {
	t.Helper()

	proc, err := env.Start(ctx, cmd, stdio)
	require.NoErrorf(t, err, "Start(%v)", cmd)
	require.NotNilf(t, proc, "Start(%v) returned a nil Process with nil error", cmd)

	return proc
}

// waitOutcome is one Wait call's result.
type waitOutcome struct {
	result invoke.Result
	err    error
}

// waitOrTimeout waits for proc with the contract deadline; a Wait that
// stays blocked past it fails the contract.
func waitOrTimeout(t T, proc invoke.Process) waitOutcome {
	t.Helper()

	done := make(chan waitOutcome, 1)

	go func() {
		res, err := proc.Wait()
		done <- waitOutcome{result: res, err: err}
	}()

	select {
	case outcome := <-done:
		return outcome
	case <-time.After(contractTimeout):
		require.Failf(t, "Wait blocked past the contract deadline",
			"Wait did not return within %v", contractTimeout)

		return waitOutcome{}
	}
}

// runCapture starts cmd with fresh output buffers, waits with the contract
// deadline, and returns the outcome plus captured stdout and stderr.
func runCapture(t T, env invoke.Environment, cmd invoke.Command) (waitOutcome, string, string) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	proc := startCommand(t.Context(), t, env, cmd, invoke.IO{Stdout: &stdout, Stderr: &stderr})
	outcome := waitOrTimeout(t, proc)

	return outcome, stdout.String(), stderr.String()
}

// runSucceeds runs cmd and fails the contract unless it exits zero.
func runSucceeds(t T, env invoke.Environment, cmd invoke.Command) string {
	t.Helper()

	outcome, stdout, stderr := runCapture(t, env, cmd)
	require.NoErrorf(t, outcome.err, "%v failed (stderr %q)", cmd, stderr)

	return stdout
}

// requireNotExitError fails the contract when a lifecycle error (cancel,
// close, transport) is misclassified as a command outcome.
func requireNotExitError(t T, err error, situation string) {
	t.Helper()

	var exitErr *invoke.ExitError

	require.NotErrorAsf(t, err, &exitErr,
		"%s surfaced as *ExitError; lifecycle errors must never be command outcomes", situation)
}

// requireNotTransport fails the contract when a terminal outcome — one a
// caller cannot safely have retried — is classified as a TransportError,
// the one family the executor does retry. It asserts rather than requires,
// so a contract can weigh several outcomes and report each violation.
func requireNotTransport(t T, err error, situation string) {
	t.Helper()

	assert.Errorf(t, err, "%s must produce an error to classify", situation)

	var te *invoke.TransportError

	assert.NotErrorAsf(t, err, &te,
		"%s is terminal and must never be retried, yet it classifies as a TransportError", situation)
}

// closeOrTimeout closes proc with the contract deadline, so a Close that
// blocks indefinitely fails the contract instead of hanging it.
func closeOrTimeout(t T, proc invoke.Process) error {
	t.Helper()

	done := make(chan error, 1)

	go func() { done <- proc.Close() }()

	select {
	case err := <-done:
		return err
	case <-time.After(contractTimeout):
		require.Failf(t, "Close blocked past the contract deadline",
			"Close did not return within %v", contractTimeout)

		return nil
	}
}

// targetProbe runs a shell probe on the target and reports whether it
// exited zero, failing the contract if the probe could not run at all.
func targetProbe(t T, env invoke.Environment, script string) bool {
	t.Helper()

	outcome, _, stderr := runCapture(t, env, invoke.Shell(script))
	if outcome.err == nil {
		return true
	}

	var exitErr *invoke.ExitError
	if errors.As(outcome.err, &exitErr) {
		return false
	}

	require.Failf(t, "a target probe could not run at all",
		"probe %q: %v (stderr %q)", script, outcome.err, stderr)

	return false
}

// requireExitError extracts the ExitError from err or fails the contract.
func requireExitError(t T, err error) *invoke.ExitError {
	t.Helper()

	var exitErr *invoke.ExitError

	require.ErrorAs(t, err, &exitErr)

	return exitErr
}

// shellQuote wraps s in single quotes for safe interpolation into a shell
// command built by a contract. Contract paths contain no single quotes;
// this guards accidents, not adversaries.
func shellQuote(s string) string {
	return "'" + s + "'"
}

// cleanupTargetPath removes a target-side path via the environment's own
// shell, ignoring failures: cleanup is best-effort by design.
func cleanupTargetPath(t T, env invoke.Environment, path string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), contractTimeout)
	defer cancel()

	if proc, err := env.Start(ctx, invoke.Shell("rm -rf "+shellQuote(path)), invoke.IO{}); err == nil {
		_, _ = proc.Wait()
	}
}
