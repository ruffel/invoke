// Package invoketest is the executable specification for invoke providers.
//
// Every [invoke.Environment] implementation, in-tree or third-party, is
// expected to pass this suite. A provider's test calls [Verify] with a
// factory that constructs a fresh environment:
//
//	func TestContracts(t *testing.T) {
//	    invoketest.Verify(t, func(t invoketest.T) invoke.Environment {
//	        env, err := myprovider.New("target")
//	        if err != nil {
//	            t.Fatalf("constructing environment: %v", err)
//	        }
//	        return env
//	    })
//	}
//
// Each contract receives its own environment from the factory, so
// contracts are independent: destructive contracts (closing the
// environment, killing processes) cannot affect later ones, and no
// ordering between contracts is significant.
//
// Optional features are governed by the provider's declared
// [invoke.Capabilities], symmetrically: a declared capability's behavior
// contracts must pass, and an undeclared capability's requests must fail
// wrapping [invoke.ErrNotSupported]. Declaring honestly is therefore the
// only way through the suite — there is no silent middle ground.
//
// A provider that does not yet satisfy a contract may skip it, loudly and
// temporarily, with [WithKnownGap].
package invoketest

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ruffel/invoke"
)

// Contract categories, used as the first segment of a contract's ID.
const (
	// CategoryCore covers basic command execution: streams, exit codes,
	// environment, working directory.
	CategoryCore = "core"

	// CategoryLifecycle covers process lifecycle: Wait idempotency,
	// cancellation, Close, and signal delivery.
	CategoryLifecycle = "lifecycle"

	// CategoryErrors covers classification into the error taxonomy.
	CategoryErrors = "errors"

	// CategoryTransfer covers Upload and Download semantics.
	CategoryTransfer = "transfer"

	// CategoryTTY covers pseudo-terminal allocation.
	CategoryTTY = "tty"
)

// T is the testing surface contracts run against. *testing.T satisfies it;
// the indirection lets the suite test its own contracts by running them
// against a recording implementation.
type T interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	FailNow()
	Skipf(format string, args ...any)
	Logf(format string, args ...any)
	Context() context.Context
	TempDir() string
	Name() string
}

// Factory constructs a fresh environment for one contract. It reports
// construction problems on t (typically via Fatalf, or Skipf when the
// target is unavailable, such as a daemon that is not running).
type Factory func(t T) invoke.Environment

// TestCase is a single behavioral contract.
type TestCase struct {
	// Category is one of the Category constants.
	Category string

	// Name identifies the contract within its category, in kebab-case.
	Name string

	// Description states the behavior the contract enforces.
	Description string

	// Gate, when non-nil, decides from the environment's declared
	// capabilities whether this contract applies. When it returns
	// false, the contract is skipped with the returned reason. Paired
	// contracts cover both sides of each capability, so a gated skip is
	// always matched by a contract that does run.
	Gate func(caps invoke.Capabilities) (bool, string)

	// Run executes the contract against env, reporting failures on t.
	Run func(t T, env invoke.Environment)
}

// ID returns the contract's stable, globally unique identifier.
func (tc TestCase) ID() string {
	return tc.Category + "/" + tc.Name
}

// Option configures a Verify run.
type Option func(*verifyConfig)

type verifyConfig struct {
	gaps map[string]string
}

// WithKnownGap marks a contract the provider does not yet satisfy. The
// contract is skipped — visibly, with the supplied reason, which should
// reference a tracking issue — instead of failing the run.
//
// It is a temporary escape hatch for landing a stricter contract before
// every provider honors it. Naming a contract ID that does not exist fails
// the run, so an opt-out cannot silently outlive a renamed or removed
// contract.
func WithKnownGap(contractID, reason string) Option {
	return func(cfg *verifyConfig) {
		cfg.gaps[contractID] = reason
	}
}

// Verify runs every contract in the suite against environments produced by
// factory. It is the standard entry point for provider test suites.
func Verify(t *testing.T, factory Factory, opts ...Option) {
	t.Helper()

	verifyContracts(t, AllContracts(), factory, opts...)
}

// AllContracts returns the full contract set, for harnesses that need to
// enumerate or filter it. The returned cases are safe to inspect; run them
// via [Verify] where possible.
func AllContracts() []TestCase {
	const expectedContracts = 64

	contracts := make([]TestCase, 0, expectedContracts)

	contracts = append(contracts, coreContracts()...)

	validateContracts(contracts)

	return contracts
}

func verifyContracts(t *testing.T, contracts []TestCase, factory Factory, opts ...Option) {
	t.Helper()

	cfg := verifyConfig{gaps: make(map[string]string)}
	for _, opt := range opts {
		opt(&cfg)
	}

	if err := validateGaps(contracts, cfg.gaps); err != nil {
		t.Fatalf("invoketest: %v", err)
	}

	for _, tc := range contracts {
		t.Run(tc.ID(), func(t *testing.T) {
			t.Helper()

			if reason, ok := cfg.gaps[tc.ID()]; ok {
				t.Skipf("known provider gap: %s", reason)
			}

			env := factory(t)

			defer func() {
				_ = env.Close()
			}()

			if tc.Gate != nil {
				if run, reason := tc.Gate(env.Capabilities()); !run {
					t.Skipf("not applicable: %s", reason)
				}
			}

			tc.Run(t, env)
		})
	}
}

// validateGaps rejects WithKnownGap declarations that name contracts which
// do not exist, so stale opt-outs fail instead of lingering.
func validateGaps(contracts []TestCase, gaps map[string]string) error {
	if len(gaps) == 0 {
		return nil
	}

	valid := make(map[string]struct{}, len(contracts))
	for _, tc := range contracts {
		valid[tc.ID()] = struct{}{}
	}

	var unknown []string

	for id := range gaps {
		if _, ok := valid[id]; !ok {
			unknown = append(unknown, id)
		}
	}

	if len(unknown) > 0 {
		return fmt.Errorf("WithKnownGap names unknown contracts: %s", strings.Join(unknown, ", "))
	}

	return nil
}

// validateContracts enforces structural invariants on the contract set;
// violations are programmer errors in the suite itself.
func validateContracts(contracts []TestCase) {
	seen := make(map[string]struct{}, len(contracts))

	for _, tc := range contracts {
		if strings.TrimSpace(tc.Category) == "" || strings.TrimSpace(tc.Name) == "" {
			panic(fmt.Sprintf("invoketest: contract with empty category or name: %+v", tc))
		}

		if tc.Run == nil {
			panic(fmt.Sprintf("invoketest: contract %q has no Run", tc.ID()))
		}

		if _, dup := seen[tc.ID()]; dup {
			panic(fmt.Sprintf("invoketest: duplicate contract id %q", tc.ID()))
		}

		seen[tc.ID()] = struct{}{}
	}
}
