package invoketest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
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

// exitSettle is the floor for how long a contract waits for a process to
// finish exiting once its last output has been observed. It is a margin
// around an event already witnessed rather than a substitute for
// witnessing one: the output says the command reached its final act, and
// this covers the short walk from there to the process being gone.
//
// A floor, because the walk is not the same length on every target.
// Contracts scale it with roundTrip.
const exitSettle = 250 * time.Millisecond

// roundTrip measures what one trivial command costs on this target, from
// Start to a settled Wait.
//
// Contracts that need a margin scale it from this rather than from a
// literal. The targets differ by orders of magnitude — an in-memory fake
// against a container the other side of a daemon, on a machine running
// several suites at once — so a margin that is generous against one is
// the flake against another.
func roundTrip(t T, env invoke.Environment) time.Duration {
	t.Helper()

	begun := time.Now()

	proc := startCommand(t.Context(), t, env, invoke.New("true"), invoke.IO{})

	_, err := proc.Wait()
	require.NoError(t, err, "measuring what one command costs on this target")

	_ = proc.Close()

	return time.Since(begun)
}

// blockingReader is a stdin whose first Read reports it began and then
// holds — the caller-supplied reader no provider can interrupt: an
// io.Pipe nobody writes to, a network stream gone quiet. Contracts
// release it on the way out so a disowned pump goroutine can end.
type blockingReader struct {
	// started closes once something is inside Read, so a contract can
	// wait for the wedge to be real rather than guess.
	started chan struct{}

	// held blocks the read until the contract releases it.
	held chan struct{}

	startOnce   sync.Once
	releaseOnce sync.Once
}

func newBlockingReader() *blockingReader {
	return &blockingReader{
		started: make(chan struct{}),
		held:    make(chan struct{}),
	}
}

// Read reports the first call, then blocks until released; a released
// read reports end of input.
func (r *blockingReader) Read(_ []byte) (int, error) {
	r.startOnce.Do(func() { close(r.started) })

	<-r.held

	return 0, io.EOF
}

// release unblocks the read.
func (r *blockingReader) release() {
	r.releaseOnce.Do(func() { close(r.held) })
}

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

// startedCommand runs script through the target's shell and returns once
// marker has appeared on stdout, so the process is provably running
// before the contract acts on it.
//
// The proof matters for signaling: a signal is fire-and-forget on some
// transports — SSH's request carries no reply, and a server that
// receives one before the command has started drops it — so a contract
// that signals immediately after Start measures the start race rather
// than delivery.
//
// A script that keeps running after its marker must exec its final
// command. A shell left waiting in front of it can absorb a later
// signal and die alone, and the orphaned child then holds the session's
// output open — a Wait that follows the pipes blocks on a process
// nobody signaled.
func startedCommand(ctx context.Context, t T, env invoke.Environment, script, marker string) invoke.Process {
	t.Helper()

	out := &lockedBuffer{}
	proc := startCommand(ctx, t, env, invoke.Shell(script), invoke.IO{Stdout: out})

	deadline := time.Now().Add(contractTimeout)
	for time.Now().Before(deadline) {
		if out.contains(marker) {
			return proc
		}

		time.Sleep(10 * time.Millisecond)
	}

	_ = proc.Close()

	require.Failf(t, "the process never printed its liveness marker",
		"%q did not appear on stdout within %v", marker, contractTimeout)

	return nil
}

// lockedBuffer is an output sink safe to read while the provider is
// still writing to it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *lockedBuffer) contains(s string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return bytes.Contains(b.buf.Bytes(), []byte(s))
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
