//go:build windows

package local

import (
	"os/exec"
	"strconv"
)

// killProcessGroup terminates the process with the given PID and its child
// processes. On Windows, this is implemented using `taskkill /T`, which kills
// the process tree rooted at the specified PID (Windows does not have POSIX
// process groups).
//
// TODO(windows): If we need proper process grouping semantics, resource limits,
// or orphan handling, consider using Windows Job Objects instead of taskkill.
// See https://learn.microsoft.com/windows/win32/procthread/job-objects
// for details.
func killProcessGroup(pid int) error {
	return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}

// setProcessGroup sets the process grouping behavior for the given command on
// Windows. Currently this is a no-op until we adopt Job Objects.
func setProcessGroup(_ *exec.Cmd) {
	// TODO(windows): Attach the process to a Job Object when we add support for
	// Windows Job Objects (see link above) to provide stronger grouping semantics.
}
