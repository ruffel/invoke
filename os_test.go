package invoke_test

import (
	"runtime"
	"testing"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
)

func TestLocalOS(t *testing.T) {
	t.Parallel()

	assert.Equal(t, runtime.GOOS, string(invoke.LocalOS()))
}

func TestTargetOSValues(t *testing.T) {
	t.Parallel()

	// The constants mirror runtime.GOOS strings; pin them so provider
	// comparisons and logs stay stable.
	assert.Equal(t, "linux", string(invoke.OSLinux), "TargetOS constants must not drift")
	assert.Equal(t, "darwin", string(invoke.OSDarwin), "TargetOS constants must not drift")
	assert.Empty(t, string(invoke.OSUnknown), "TargetOS constants must not drift")
}
