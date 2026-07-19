package invoke_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExitErrorMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  *invoke.ExitError
		want string
	}{
		{
			name: "non-zero exit",
			err:  &invoke.ExitError{Code: 3},
			want: "process exited with code 3",
		},
		{
			name: "signal termination",
			err:  &invoke.ExitError{Code: -1, Signal: invoke.SIGKILL},
			want: "process terminated by signal KILL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, tt.err.Error())
		})
	}
}

func TestExitErrorExtractionThroughWrapping(t *testing.T) {
	t.Parallel()

	exitErr := &invoke.ExitError{Code: 7}
	wrapped := fmt.Errorf("run %q: %w", "deploy", exitErr)

	var got *invoke.ExitError

	require.ErrorAs(t, wrapped, &got)

	assert.Equal(t, 7, got.Code)
}

func TestTransportErrorUnwrapsToRootCause(t *testing.T) {
	t.Parallel()

	root := errors.New("connection reset by peer")
	err := &invoke.TransportError{Op: "start", Err: fmt.Errorf("dial: %w", root)}

	assert.ErrorIs(t, err, root, "errors.Is must reach the root cause through a TransportError")

	want := "transport failure during start: dial: connection reset by peer"

	assert.Equal(t, want, err.Error())
}

func TestTaxonomyFamiliesAreDisjoint(t *testing.T) {
	t.Parallel()

	// Each error is classified into exactly one family; the assertions
	// callers rely on for control flow must never cross-match.
	var (
		exitErr      error = &invoke.ExitError{Code: 1}
		transportErr error = &invoke.TransportError{Op: "start", Err: errors.New("boom")}
		closedErr          = fmt.Errorf("start: %w", invoke.ErrClosed)
		canceledErr        = fmt.Errorf("wait: %w", context.Canceled)
	)

	var asExit *invoke.ExitError

	var asTransport *invoke.TransportError

	assert.NotErrorAs(t, transportErr, &asExit, "TransportError must not match *ExitError")

	assert.NotErrorAs(t, exitErr, &asTransport, "ExitError must not match *TransportError")

	assert.NotErrorIs(t, exitErr, invoke.ErrClosed, "ExitError must not match a lifecycle sentinel")
	assert.NotErrorIs(t, exitErr, context.Canceled, "ExitError must not match a context error")

	assert.NotErrorAs(t, closedErr, &asExit, "sentinel/context errors must not match *ExitError")
	assert.NotErrorAs(t, canceledErr, &asExit, "sentinel/context errors must not match *ExitError")
}

func TestSentinelsSurviveWrapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		sentinel error
	}{
		{name: "closed", sentinel: invoke.ErrClosed},
		{name: "not supported", sentinel: invoke.ErrNotSupported},
		{name: "not found", sentinel: invoke.ErrNotFound},
		{name: "invalid workdir", sentinel: invoke.ErrInvalidWorkdir},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			wrapped := fmt.Errorf("start %q on host %q: %w", "deploy", "web-1", tt.sentinel)

			assert.ErrorIs(t, wrapped, tt.sentinel, "the sentinel must survive wrapping")
		})
	}
}
