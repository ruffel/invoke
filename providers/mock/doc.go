// Package mock provides a controllable implementation of invoke.Environment
// for testing purposes.
//
// It allows defining expectations for command execution and file operations,
// enabling deterministic unit tests for code that builds upon the invoke library.
//
// Usage:
//
//	m := mock.New()
//	m.OnCommand("git status").Return(0, "On branch main", "")
//	// pass 'm' to your logic
package mock
