package invoketest

import (
	"context"
	"fmt"
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
type T interface {
	Errorf(format string, args ...any)
	FailNow()
	Skipf(format string, args ...any)
	Context() context.Context
	TempDir() string
	Name() string
}

// TestCase defines a single behavioral contract requirement.
type TestCase struct {
	Category    string
	Name        string
	Description string
	Prereq      func(t T, env invoke.Environment) (ok bool, reason string)
	Run         func(t T, env invoke.Environment)
}

// ID returns the stable, globally unique contract identifier.
func (tc TestCase) ID() string {
	return fmt.Sprintf("%s/%s", tc.Category, tc.Name)
}

// Verify is the standard Go test entry point for provider authors.
func Verify(t *testing.T, env invoke.Environment) {
	t.Helper()

	for _, tc := range AllContracts() {
		t.Run(tc.ID(), func(t *testing.T) {
			if tc.Prereq != nil {
				ok, reason := tc.Prereq(t, env)
				if !ok {
					t.Skipf("prereq unmet: %s", reason)
				}
			}

			tc.Run(t, env)
		})
	}
}
