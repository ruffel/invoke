//go:build windows

package local

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/ruffel/invoke"
)

// Windows is not a supported execution target this cycle; New refuses to
// construct an Environment there, so these stubs exist only to keep the
// package compiling for cross-builds.

func setProcessGroup(_ *exec.Cmd) {}

func killProcessGroup(_ int) error {
	return fmt.Errorf("local: process groups on windows: %w", invoke.ErrNotSupported)
}

func signalGroup(_ int, _ syscall.Signal) error {
	return fmt.Errorf("local: signals on windows: %w", invoke.ErrNotSupported)
}

func sysSignal(_ invoke.Signal) (syscall.Signal, bool) {
	return 0, false
}

func exitSignal(_ *os.ProcessState) (invoke.Signal, bool) {
	return "", false
}

// dirEnterable reports whether the directory can be entered. Execution
// semantics are out of scope on Windows, and its ACL model has no
// access(2); an existing directory is taken at its word.
func dirEnterable(_ string) bool { return true }
