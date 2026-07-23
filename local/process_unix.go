//go:build unix

package local

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"github.com/ruffel/invoke"
)

// setProcessGroup places the command in its own process group, so kills
// and signals reach the command's whole tree without touching the caller.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup delivers SIGKILL to pid's process group. A group that
// no longer exists reports os.ErrProcessDone.
func killProcessGroup(pid int) error {
	return groupSignal(pid, syscall.SIGKILL)
}

// signalGroup delivers sig to pid's process group.
func signalGroup(pid int, sig syscall.Signal) error {
	return groupSignal(pid, sig)
}

func groupSignal(pid int, sig syscall.Signal) error {
	if err := syscall.Kill(-pid, sig); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}

		return err
	}

	return nil
}

// sysSignal maps a portable signal name onto its local syscall number.
func sysSignal(sig invoke.Signal) (syscall.Signal, bool) {
	switch sig {
	case invoke.SIGINT:
		return syscall.SIGINT, true
	case invoke.SIGTERM:
		return syscall.SIGTERM, true
	case invoke.SIGKILL:
		return syscall.SIGKILL, true
	case invoke.SIGHUP:
		return syscall.SIGHUP, true
	case invoke.SIGQUIT:
		return syscall.SIGQUIT, true
	case invoke.SIGUSR1:
		return syscall.SIGUSR1, true
	case invoke.SIGUSR2:
		return syscall.SIGUSR2, true
	default:
		return 0, false
	}
}

// exitSignal reports the signal that terminated the process, if one did.
func exitSignal(state *os.ProcessState) (invoke.Signal, bool) {
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return "", false
	}

	return signalName(status.Signal()), true
}

// signalName renders a syscall signal as a portable name. Signals outside
// the supported set (a segfault, for instance) are still reported honestly
// by name or number rather than collapsed into something misleading.
func signalName(sig syscall.Signal) invoke.Signal {
	switch sig {
	case syscall.SIGINT:
		return invoke.SIGINT
	case syscall.SIGTERM:
		return invoke.SIGTERM
	case syscall.SIGKILL:
		return invoke.SIGKILL
	case syscall.SIGHUP:
		return invoke.SIGHUP
	case syscall.SIGQUIT:
		return invoke.SIGQUIT
	case syscall.SIGUSR1:
		return invoke.SIGUSR1
	case syscall.SIGUSR2:
		return invoke.SIGUSR2
	case syscall.SIGSEGV:
		return invoke.Signal("SEGV")
	case syscall.SIGABRT:
		return invoke.Signal("ABRT")
	case syscall.SIGPIPE:
		return invoke.Signal("PIPE")
	case syscall.SIGALRM:
		return invoke.Signal("ALRM")
	case syscall.SIGBUS:
		return invoke.Signal("BUS")
	case syscall.SIGFPE:
		return invoke.Signal("FPE")
	case syscall.SIGILL:
		return invoke.Signal("ILL")
	default:
		return invoke.Signal(strconv.Itoa(int(sig)))
	}
}

// dirEnterable reports whether the directory's mode lets this process
// enter it, which is what a working directory has to allow.
func dirEnterable(path string) bool {
	return syscall.Access(path, execPermission) == nil
}

// execPermission is access(2)'s X_OK: permission to traverse.
const execPermission = 0x1
