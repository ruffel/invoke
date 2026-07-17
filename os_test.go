package invoke_test

import (
	"runtime"
	"testing"

	"github.com/ruffel/invoke"
)

func TestLocalOS(t *testing.T) {
	t.Parallel()

	if got := invoke.LocalOS(); string(got) != runtime.GOOS {
		t.Errorf("LocalOS() = %q, want %q", got, runtime.GOOS)
	}
}

func TestTargetOSValues(t *testing.T) {
	t.Parallel()

	// The constants mirror runtime.GOOS strings; pin them so provider
	// comparisons and logs stay stable.
	if invoke.OSLinux != "linux" || invoke.OSDarwin != "darwin" || invoke.OSUnknown != "" {
		t.Errorf("TargetOS constants drifted: linux=%q darwin=%q unknown=%q",
			invoke.OSLinux, invoke.OSDarwin, invoke.OSUnknown)
	}
}
