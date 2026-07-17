//go:build windows

package invoketest

// makeFIFO reports that named pipes for the special-file contract are not
// available on Windows hosts; the contract skips.
func makeFIFO(t T, _ string) bool {
	t.Helper()

	return false
}
