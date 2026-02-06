//go:build !windows

package local

import (
	"os/exec"
	"syscall"
)

// killProcessGroup kills the process group with the given PID.
func killProcessGroup(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}

// setProcessGroup sets the process group for the given command.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
