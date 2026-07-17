package invoke_test

import (
	"testing"

	"github.com/ruffel/invoke"
)

func TestSignalNames(t *testing.T) {
	t.Parallel()

	// Signal values are wire-level identifiers (SSH signal names, kill -s
	// arguments); pin them so they cannot drift.
	want := map[invoke.Signal]string{
		invoke.SIGINT:  "INT",
		invoke.SIGTERM: "TERM",
		invoke.SIGKILL: "KILL",
		invoke.SIGHUP:  "HUP",
		invoke.SIGQUIT: "QUIT",
		invoke.SIGUSR1: "USR1",
		invoke.SIGUSR2: "USR2",
	}

	for sig, name := range want {
		if string(sig) != name {
			t.Errorf("signal %q drifted from wire name %q", sig, name)
		}
	}
}
