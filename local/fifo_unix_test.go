//go:build unix

package local_test

import "syscall"

// makeFIFO creates a named pipe for the special-file tests.
func makeFIFO(path string, mode uint32) error {
	return syscall.Mkfifo(path, mode)
}
