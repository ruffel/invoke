//go:build unix

package invoketest

import "syscall"

// makeFIFO creates a named pipe for the special-file contract, reporting
// whether the host supports it.
func makeFIFO(t T, path string) bool {
	t.Helper()

	return syscall.Mkfifo(path, 0o600) == nil
}
