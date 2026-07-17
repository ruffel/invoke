//go:build unix

package ssh_test

import (
	"os"
	"syscall"
)

// sigTERM is the SSH name for SIGTERM, used as the default when a host
// signal has no portable name.
const sigTERM = "TERM"

// signalToSyscall maps an SSH signal name to the host syscall signal, for
// the test server to deliver.
func signalToSyscall(name string) (os.Signal, bool) {
	switch name {
	case "INT":
		return syscall.SIGINT, true
	case sigTERM:
		return syscall.SIGTERM, true
	case "KILL":
		return syscall.SIGKILL, true
	case "HUP":
		return syscall.SIGHUP, true
	case "QUIT":
		return syscall.SIGQUIT, true
	case "USR1":
		return syscall.SIGUSR1, true
	case "USR2":
		return syscall.SIGUSR2, true
	default:
		return nil, false
	}
}

// sysToSignalName maps a host syscall signal back to its SSH name, for the
// test server's exit-signal report.
func sysToSignalName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGINT:
		return "INT"
	case syscall.SIGTERM:
		return sigTERM
	case syscall.SIGKILL:
		return "KILL"
	case syscall.SIGHUP:
		return "HUP"
	case syscall.SIGQUIT:
		return "QUIT"
	case syscall.SIGUSR1:
		return "USR1"
	case syscall.SIGUSR2:
		return "USR2"
	default:
		return sigTERM
	}
}
