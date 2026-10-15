package local_test

import (
	"testing"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/invoketest"
	"github.com/ruffel/invoke/local"
	"github.com/stretchr/testify/require"
)

// TestLocalPassesContractSuite holds the reference provider to the
// published spec from its own package, where anyone changing it runs
// it.
//
// invoketest checks its own contracts against local, but through the
// wrapper its defect catalogue is built on and from invoketest's
// package, so a change here was only held to the spec by whoever also
// ran that package's tests.
func TestLocalPassesContractSuite(t *testing.T) {
	t.Parallel()

	invoketest.Verify(t, func(it invoketest.T) invoke.Environment {
		env, err := local.New()
		require.NoError(it, err, "local.New()")

		return env
	})
}
