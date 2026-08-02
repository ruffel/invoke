//go:build windows

package local_test

import "errors"

// makeFIFO reports that named pipes are unavailable, so the special-file
// tests skip. Windows is a cross-compilation target for this repository
// rather than an execution one; the stub exists so the package still
// typechecks there.
func makeFIFO(string, uint32) error {
	return errors.New("named pipes are not available on Windows")
}
