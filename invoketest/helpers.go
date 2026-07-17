package invoketest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/ruffel/invoke"
)

// contractTimeout bounds every blocking step inside a contract, so a
// provider that hangs produces a failed contract rather than a hung suite.
const contractTimeout = 5 * time.Second

// failf reports a contract failure and stops the contract.
func failf(t T, format string, args ...any) {
	t.Helper()
	t.Errorf(format, args...)
	t.FailNow()
}

// token returns a short random hex string for unique target-side paths.
func token(t T) string {
	t.Helper()

	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		failf(t, "generating random token: %v", err)
	}

	return hex.EncodeToString(raw[:])
}

// startCommand starts cmd and fails the contract on error.
func startCommand(ctx context.Context, t T, env invoke.Environment, cmd invoke.Command, stdio invoke.IO) invoke.Process {
	t.Helper()

	proc, err := env.Start(ctx, cmd, stdio)
	if err != nil {
		failf(t, "Start(%v) = %v", cmd, err)
	}

	if proc == nil {
		failf(t, "Start(%v) returned a nil Process with nil error", cmd)
	}

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
		failf(t, "Wait did not return within %v", contractTimeout)

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
	if outcome.err != nil {
		failf(t, "%v failed: %v (stderr %q)", cmd, outcome.err, stderr)
	}

	return stdout
}

// requireNotExitError fails the contract when a lifecycle error (cancel,
// close, transport) is misclassified as a command outcome.
func requireNotExitError(t T, err error, situation string) {
	t.Helper()

	var exitErr *invoke.ExitError
	if errors.As(err, &exitErr) {
		failf(t, "%s surfaced as *ExitError (%v); lifecycle errors must never be command outcomes", situation, exitErr)
	}
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
		failf(t, "Close did not return within %v", contractTimeout)

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

	failf(t, "probe %q could not run: %v (stderr %q)", script, outcome.err, stderr)

	return false
}

// requireExitError extracts the ExitError from err or fails the contract.
func requireExitError(t T, err error) *invoke.ExitError {
	t.Helper()

	var exitErr *invoke.ExitError
	if !errors.As(err, &exitErr) {
		failf(t, "error = %v, want *invoke.ExitError", err)
	}

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
