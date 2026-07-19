package invoketest

import (
	"context"
	"testing"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubEnv is a minimal Environment for exercising the suite mechanics; it
// records closes and reports configurable capabilities.
type stubEnv struct {
	caps   invoke.Capabilities
	closed bool
}

func (s *stubEnv) Start(_ context.Context, _ invoke.Command, _ invoke.IO) (invoke.Process, error) {
	return nil, invoke.ErrNotSupported
}

func (s *stubEnv) LookPath(_ context.Context, name string) (string, error) { return name, nil }

func (s *stubEnv) Upload(_ context.Context, _, _ string, _ ...invoke.TransferOption) error {
	return nil
}

func (s *stubEnv) Download(_ context.Context, _, _ string, _ ...invoke.TransferOption) error {
	return nil
}

func (s *stubEnv) OS() invoke.TargetOS               { return invoke.OSLinux }
func (s *stubEnv) Capabilities() invoke.Capabilities { return s.caps }

func (s *stubEnv) Close() error {
	s.closed = true

	return nil
}

//nolint:tparallel // Contracts run sequentially by design: they may share a real target.
func TestVerifyGivesEachContractAFreshEnvironment(t *testing.T) {
	t.Parallel()

	var (
		created []*stubEnv
		ran     []string
	)

	contracts := []TestCase{
		{Category: "core", Name: "one", Run: func(_ T, _ invoke.Environment) { ran = append(ran, "one") }},
		{Category: "core", Name: "two", Run: func(_ T, _ invoke.Environment) { ran = append(ran, "two") }},
		{Category: "core", Name: "three", Run: func(_ T, _ invoke.Environment) { ran = append(ran, "three") }},
	}

	verifyContracts(t, contracts, func(_ T) invoke.Environment {
		env := &stubEnv{}
		created = append(created, env)

		return env
	})

	require.Len(t, ran, 3, "every contract must run")
	require.Len(t, created, 3, "the factory must be called once per contract: one fresh environment each")

	for i, env := range created {
		assert.Truef(t, env.closed, "environment %d was not closed after its contract", i)
	}
}

//nolint:tparallel // Contracts run sequentially by design: they may share a real target.
func TestVerifySkipsKnownGaps(t *testing.T) {
	t.Parallel()

	var ran []string

	contracts := []TestCase{
		{Category: "core", Name: "kept", Run: func(_ T, _ invoke.Environment) { ran = append(ran, "kept") }},
		{Category: "core", Name: "gapped", Run: func(_ T, _ invoke.Environment) { ran = append(ran, "gapped") }},
	}

	verifyContracts(t, contracts,
		func(_ T) invoke.Environment { return &stubEnv{} },
		WithKnownGap("core/gapped", "tracked: not implemented yet"),
	)

	assert.Equal(t, []string{"kept"}, ran, "only the non-gapped contract may run")
}

func TestValidateGaps(t *testing.T) {
	t.Parallel()

	contracts := []TestCase{
		{Category: "core", Name: "exists", Run: func(_ T, _ invoke.Environment) {}},
	}

	assert.NoError(t, validateGaps(contracts, map[string]string{"core/exists": "ok"}), "valid gap rejected")

	err := validateGaps(contracts, map[string]string{"core/renamed-away": "stale"})
	require.Error(t, err, "unknown gap ID accepted; stale opt-outs must fail the run")

	assert.ErrorContains(t, err, "core/renamed-away", "the error must name the unknown contract")
}

func TestVerifyGatesOnCapabilities(t *testing.T) {
	t.Parallel()

	gated := TestCase{
		Category: "tty",
		Name:     "gated",
		Gate: func(caps invoke.Capabilities) (bool, string) {
			return caps.TTY, "target declares TTY unsupported; the unsupported contract covers it"
		},
		Run: func(_ T, _ invoke.Environment) {},
	}

	t.Run("capability declared", func(t *testing.T) {
		t.Parallel()

		ran := false
		tc := gated
		tc.Run = func(_ T, _ invoke.Environment) { ran = true }

		verifyContracts(t, []TestCase{tc}, func(_ T) invoke.Environment {
			return &stubEnv{caps: invoke.Capabilities{TTY: true}}
		})

		assert.True(t, ran, "contract gated on a declared capability did not run")
	})

	t.Run("capability not declared", func(t *testing.T) {
		t.Parallel()

		ran := false
		tc := gated
		tc.Run = func(_ T, _ invoke.Environment) { ran = true }

		verifyContracts(t, []TestCase{tc}, func(_ T) invoke.Environment {
			return &stubEnv{caps: invoke.Capabilities{TTY: false}}
		})

		assert.False(t, ran, "contract gated on an undeclared capability ran anyway")
	})
}

func TestValidateContractsRejectsDuplicates(t *testing.T) {
	t.Parallel()

	assert.Panics(t, func() {
		validateContracts([]TestCase{
			{Category: "core", Name: "same", Run: func(_ T, _ invoke.Environment) {}},
			{Category: "core", Name: "same", Run: func(_ T, _ invoke.Environment) {}},
		})
	}, "duplicate contract IDs did not panic")
}

func TestValidateContractsRejectsMissingRun(t *testing.T) {
	t.Parallel()

	assert.Panics(t, func() {
		validateContracts([]TestCase{{Category: "core", Name: "hollow"}})
	}, "contract without Run did not panic")
}

func TestAllContractsIsStructurallyValid(t *testing.T) {
	t.Parallel()

	// AllContracts panics on structural violations; calling it is the
	// assertion. It also pins that every registered category is known.
	known := map[string]bool{
		CategoryCore:      true,
		CategoryLifecycle: true,
		CategoryErrors:    true,
		CategoryTransfer:  true,
		CategoryTTY:       true,
	}

	for _, tc := range AllContracts() {
		assert.Truef(t, known[tc.Category], "contract %q uses unknown category %q", tc.ID(), tc.Category)
		assert.NotEmptyf(t, tc.Description, "contract %q has no description", tc.ID())
	}
}
