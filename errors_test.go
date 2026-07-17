package invoke_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ruffel/invoke"
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

			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExitErrorExtractionThroughWrapping(t *testing.T) {
	t.Parallel()

	exitErr := &invoke.ExitError{Code: 7}
	wrapped := fmt.Errorf("run %q: %w", "deploy", exitErr)

	var got *invoke.ExitError
	if !errors.As(wrapped, &got) {
		t.Fatalf("errors.As failed to extract *ExitError from %v", wrapped)
	}

	if got.Code != 7 {
		t.Errorf("extracted Code = %d, want 7", got.Code)
	}
}

func TestTransportErrorUnwrapsToRootCause(t *testing.T) {
	t.Parallel()

	root := errors.New("connection reset by peer")
	err := &invoke.TransportError{Op: "start", Err: fmt.Errorf("dial: %w", root)}

	if !errors.Is(err, root) {
		t.Errorf("errors.Is failed to reach the root cause through %v", err)
	}

	want := "transport failure during start: dial: connection reset by peer"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
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

	if errors.As(transportErr, &asExit) {
		t.Error("TransportError must not match *ExitError")
	}

	if errors.As(exitErr, &asTransport) {
		t.Error("ExitError must not match *TransportError")
	}

	if errors.Is(exitErr, invoke.ErrClosed) || errors.Is(exitErr, context.Canceled) {
		t.Error("ExitError must not match sentinel or context errors")
	}

	if errors.As(closedErr, &asExit) || errors.As(canceledErr, &asExit) {
		t.Error("sentinel/context errors must not match *ExitError")
	}
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
			if !errors.Is(wrapped, tt.sentinel) {
				t.Errorf("errors.Is(%v, sentinel) = false after wrapping", wrapped)
			}
		})
	}
}
