package invoketest

import (
	"context"
	"testing"

	"github.com/ruffel/invoke"
)

// Standard categories for grouping tests.
const (
	CategoryCore        = "core"
	CategoryEnvironment = "environment"
	CategoryFilesystem  = "filesystem"
	CategorySystem      = "system"
	CategoryErrors      = "errors"
)

// T is the minimal interface required for testify/assert and require.
// *testing.T satisfies this interface automatically (Go 1.24+).
type T interface {
	Errorf(format string, args ...any)
	FailNow()
	Context() context.Context
	TempDir() string
}

// TestCase defines a single behavioral contract requirement.
type TestCase struct {
	Category    string
	Name        string
	Description string
	Run         func(t T, env invoke.Environment)
}

// Verify is the standard Go test entry point for provider authors.
func Verify(t *testing.T, env invoke.Environment) {
	t.Helper()

	for _, tc := range AllContracts() {
		t.Run(tc.Category+"/"+tc.Name, func(t *testing.T) {
			tc.Run(t, env)
		})
	}
}
