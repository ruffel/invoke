//go:build windows

package local

import (
	"os/exec"
	"strconv"
)

// killProcessGroup kills the process group with the given PID.
//
// TODO(windows): Use Job Objects for proper process grouping if we need
// resource limits or orphan handling. For now, taskkill /T is good enough
// because that's how other tools do it. [citation needed].
func killProcessGroup(pid int) error {
	return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}

// setProcessGroup sets the process group for the given command.
func setProcessGroup(_ *exec.Cmd) {
	// TODO(windows): Nothing to do until we use Job Objects.
}
