//go:build unix

package local_test

import (
	"os"
	"testing"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/local"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openFDs counts this process's open file descriptors. Readdirnames
// never stats, which matters on darwin: devfs reports no entry types,
// and a stat fallback trips over the directory handle's own descriptor.
func openFDs(t *testing.T) int {
	t.Helper()

	dir, err := os.Open("/dev/fd")
	require.NoError(t, err, "counting descriptors")

	defer func() { _ = dir.Close() }()

	names, err := dir.Readdirnames(-1)
	require.NoError(t, err, "counting descriptors")

	return len(names)
}

// TestFailedTTYStartLeaksNoDescriptors pins the cleanup of a terminal
// whose command never ran. The exec package does not close
// caller-supplied files when Start fails, so the command's end of the
// terminal is still this side's to release — a retry loop against a
// missing binary would otherwise bleed a descriptor per attempt until
// a garbage collection happened to notice.
//
// The test stays serial: it counts the process's descriptors, and a
// parallel test opening files would count too.
//
//nolint:paralleltest // Descriptor counting needs the process to itself.
func TestFailedTTYStartLeaksNoDescriptors(t *testing.T) {
	env, err := local.New()
	require.NoError(t, err)

	t.Cleanup(func() { _ = env.Close() })

	startOnce := func() {
		t.Helper()

		proc, startErr := env.Start(t.Context(), invoke.New("/nonexistent/tool"), invoke.IO{TTY: &invoke.TTY{}})
		if startErr == nil {
			_ = proc.Close()

			require.Fail(t, "the binary does not exist; Start cannot succeed")
		}
	}

	// One warm-up start, so lazy initialization is not counted.
	startOnce()

	before := openFDs(t)

	const attempts = 10

	for range attempts {
		startOnce()
	}

	assert.Equal(t, before, openFDs(t),
		"failed TTY starts must release both ends of the terminal")
}
