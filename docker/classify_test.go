package docker

import (
	"errors"
	"testing"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClassifyStartReadsTheRightPattern pins classification against the
// daemon's real message shapes. The hazard is order: runc's workdir
// failure ends in "no such file or directory", so a classifier that
// checks the missing-binary patterns first blames the executable for a
// directory that does not exist.
func TestClassifyStartReadsTheRightPattern(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		message  string
		sentinel error
	}{
		{
			name: "a missing workdir is the workdir's fault",
			message: `OCI runtime create failed: runc create failed: unable to start container process: ` +
				`chdir to cwd ("/missing") set in config.json failed: no such file or directory: unknown`,
			sentinel: invoke.ErrInvalidWorkdir,
		},
		{
			name: "a workdir that is a file is the workdir's fault",
			message: `OCI runtime create failed: runc create failed: unable to start container process: ` +
				`chdir to cwd ("/etc/hosts") set in config.json failed: not a directory: unknown`,
			sentinel: invoke.ErrInvalidWorkdir,
		},
		{
			name: "a missing executable is the executable's fault",
			message: `OCI runtime exec failed: exec failed: unable to start container process: ` +
				`exec: "no-such-tool": executable file not found in $PATH: unknown`,
			sentinel: invoke.ErrNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := classifyStart(errors.New(tt.message))

			assert.ErrorIs(t, err, tt.sentinel, "daemon message: %s", tt.message)
		})
	}

	t.Run("an unrecognized failure stays a transport error", func(t *testing.T) {
		t.Parallel()

		err := classifyStart(errors.New("the daemon fell over"))

		var transportErr *invoke.TransportError

		require.ErrorAs(t, err, &transportErr,
			"an unclassified start failure is the transport's to explain")
	})
}
