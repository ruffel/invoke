//go:build windows

package ssh_test

import (
	"os"
	"syscall"
)

// sigTERM is the SSH name for SIGTERM, used as the default when a host
// signal has no portable name.
const sigTERM = "TERM"

// signalToSyscall reports that no host signal is available. Windows is a
// cross-compilation target for this repository rather than an execution
// one, so the test server never delivers one; the stub exists so the
// package still typechecks there.
func signalToSyscall(string) (os.Signal, bool) {
	return nil, false
}

// sysToSignalName answers with the default name, for the same reason: a
// Windows host reports no signalled exit for the server to translate.
func sysToSignalName(syscall.Signal) string {
	return sigTERM
}
