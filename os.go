package invoke

import "runtime"

// TargetOS identifies the operating system of an execution target. Values
// mirror runtime.GOOS strings, so a TargetOS can be compared against GOOS
// constants and logged directly.
type TargetOS string

// Known target operating systems. Execution semantics are POSIX-only this
// cycle; providers reject targets they cannot serve correctly.
const (
	// OSUnknown means the target's operating system has not been
	// determined.
	OSUnknown TargetOS = ""

	// OSLinux is the Linux kernel.
	OSLinux TargetOS = "linux"

	// OSDarwin is macOS.
	OSDarwin TargetOS = "darwin"
)

// LocalOS returns the TargetOS of the running process.
func LocalOS() TargetOS {
	return TargetOS(runtime.GOOS)
}
