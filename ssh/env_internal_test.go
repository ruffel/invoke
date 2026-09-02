package ssh

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// TestDefaultEnvRouteKeepsValuesOffTheCommandLine pins the property the
// file-based route exists for.
//
// The command line becomes the argv of a process on the remote host, and
// a process's arguments are readable by every account there. The default
// route must therefore mention only where to read the environment from,
// never what is in it.
func TestDefaultEnvRouteKeepsValuesOffTheCommandLine(t *testing.T) {
	t.Parallel()

	const secret = "s3cr3t-value"

	cmd := invoke.New("printenv", "TOKEN")

	line := commandLine(cmd, sourcePrologue("/tmp/.invoke-env-abc123"))

	assert.NotContains(t, line, secret, "the default route must not carry values on the command line")
	assert.Contains(t, line, "/tmp/.invoke-env-abc123", "the command line must say where to read them from")
	assert.Contains(t, line, "rm -f", "the file must be removed once read")

	// The values live in the file, which is created readable only by the
	// login user.
	script := exportScript([]string{"TOKEN=" + secret})
	assert.Contains(t, script, secret, "the file is what carries the values")
	assert.Contains(t, script, "export TOKEN=", "they must be exported to survive the exec")
}

// TestCommandLineEnvRouteCarriesValuesAndSaysSo pins the opt-in route's
// cost: the values really are on the command line, which is the whole
// reason it is not the default.
func TestCommandLineEnvRouteCarriesValuesAndSaysSo(t *testing.T) {
	t.Parallel()

	const secret = "s3cr3t-value"

	line := commandLine(invoke.New("printenv", "TOKEN"), exportPrologue([]string{"TOKEN=" + secret}))

	assert.Contains(t, line, secret, "the opt-in route carries values on the command line by definition")
}

// TestEnvValuesAreQuotedOnEitherRoute checks a value cannot break out of
// its assignment and become script, whichever route carries it.
func TestEnvValuesAreQuotedOnEitherRoute(t *testing.T) {
	t.Parallel()

	hostile := []string{"EVIL=x'; touch /tmp/pwned; echo '"}

	for name, rendered := range map[string]string{
		"file":         exportScript(hostile),
		"command line": exportPrologue(hostile),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			require.NotContains(t, rendered, "; touch /tmp/pwned; echo ;",
				"the value escaped its quoting and became script")

			// Everything after the first quote up to the closing one is
			// literal, so the injected text survives as data.
			assert.Contains(t, rendered, `'\''`,
				"an embedded quote must be escaped, not passed through")
		})
	}
}

// TestWaitFailureWithoutAStatusIsTerminal pins the classification of a
// command interrupted before it reported a status.
//
// The end-to-end test cannot reach this: severing the connection in the
// unit lane reliably produces ExitMissingError, which was terminal
// already. The shapes below are the ones the same outage produces when a
// different part of the session notices it first, and which one a caller
// gets is not something they chose. All of them must classify alike, or
// the executor retries an arbitrary command on a coin flip.
func TestWaitFailureWithoutAStatusIsTerminal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
	}{
		{name: "missing exit status", err: &ssh.ExitMissingError{}},
		{name: "stream ended", err: io.EOF},
		{name: "channel closed", err: errors.New("ssh: channel closed")},
		{name: "connection reset", err: &net.OpError{Op: "read", Err: errors.New("connection reset by peer")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			proc := &process{ctxErr: func() error { return nil }}

			result, err := proc.mapOutcome(tt.err, time.Second)
			require.Error(t, err, "a wait that learned no status reported success")

			var transportErr *invoke.TransportError

			assert.NotErrorAs(t, err, &transportErr,
				"a command interrupted before it reported a status must not be retryable: "+
					"it may already have taken effect")

			assert.ErrorIs(t, err, tt.err, "the underlying failure must stay reachable")

			assert.ErrorContains(t, err, "may or may not",
				"the error does not report the outcome as unknown")

			assert.Equal(t, -1, result.ExitCode, "an unknown outcome must not carry a real-looking exit code")
		})
	}
}

// TestSignalDeathDuringTeardownIsTheCallers pins the attribution the #67
// fix turns on: teardown kills with SIGKILL but also closes the session,
// and the close can be what ends the command — the server hangs it up
// and reports a HUP or PIPE that outran the kill. While a Close or
// cancellation is in flight, that signal death is the teardown's, not
// the command's.
//
// It exercises attribute directly, with the signal it could never
// otherwise see: a real *ssh.ExitError carrying a signal cannot be
// constructed outside golang.org/x/crypto/ssh, and end to end the death
// only wins the race against the client's own Close on a slow link —
// which is why the bug reached CI as an intermittent failure and passed
// every fast local run. Here the race is settled by construction.
func TestSignalDeathDuringTeardownIsTheCallers(t *testing.T) {
	t.Parallel()

	// A signal the teardown's channel close inflicts (SIGHUP) and the
	// SIGKILL the teardown also sends: both must read as the caller's.
	for _, sig := range []invoke.Signal{invoke.SIGHUP, invoke.SIGKILL} {
		t.Run("closed/"+string(sig), func(t *testing.T) {
			t.Parallel()

			proc := &process{ctxErr: func() error { return nil }}
			proc.closedByUser.Store(true)

			_, err := proc.attribute(nil, sig, true, time.Second)

			require.ErrorIs(t, err, invoke.ErrClosed,
				"a signal death while Close is in flight belongs to the caller, not the command")

			var exitErr *invoke.ExitError

			require.NotErrorAs(t, err, &exitErr,
				"reporting the teardown's signal as the command's outcome is the #67 regression")
		})

		t.Run("canceled/"+string(sig), func(t *testing.T) {
			t.Parallel()

			proc := &process{ctxErr: func() error { return context.Canceled }}

			_, err := proc.attribute(nil, sig, true, time.Second)

			require.ErrorIs(t, err, context.Canceled,
				"a signal death while the context is canceled belongs to the cancellation")

			var exitErr *invoke.ExitError

			require.NotErrorAs(t, err, &exitErr,
				"reporting the teardown's signal as the command's outcome is the #67 regression")
		})
	}
}

// TestSignalDeathWithoutTeardownIsTheCommands pins the other side of the
// same boundary, so the fix is not later widened into swallowing a real
// signal death: with no Close or cancellation in flight, a signal is the
// command's own outcome and must surface as an ExitError naming it.
func TestSignalDeathWithoutTeardownIsTheCommands(t *testing.T) {
	t.Parallel()

	proc := &process{ctxErr: func() error { return nil }}

	_, err := proc.attribute(nil, invoke.SIGKILL, true, time.Second)

	var exitErr *invoke.ExitError

	require.ErrorAs(t, err, &exitErr,
		"a signal with no teardown in flight is the command's own death")
	assert.Equal(t, invoke.SIGKILL, exitErr.Signal, "the reported signal must be the one that landed")
	assert.Equal(t, -1, exitErr.Code, "a signal death reports Code -1")
}
