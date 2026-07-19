package invoketest

// The suite's own tests: every contract must pass against the clean
// reference environment and provably fail against its registered defect.
// Together these keep can't-fail tests out of the suite permanently.

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// selfTestTimeout backstops a contract that hangs outright; contracts
// carry their own tighter deadlines.
const selfTestTimeout = 30 * time.Second

// recordingT implements T, capturing failures instead of failing the real
// test, so contracts can be executed as subjects rather than as tests.
type recordingT struct {
	mu       sync.Mutex
	name     string
	ctx      context.Context //nolint:containedctx // recordingT implements T, whose Context method requires carrying one.
	failed   bool
	skipped  bool
	logs     []string
	tempDirs []string
}

func (r *recordingT) Helper() {}

func (r *recordingT) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.failed = true
	r.logs = append(r.logs, fmt.Sprintf(format, args...))
}

func (r *recordingT) Fatalf(format string, args ...any) {
	r.Errorf(format, args...)
	r.FailNow()
}

func (r *recordingT) FailNow() {
	r.mu.Lock()
	r.failed = true
	r.mu.Unlock()

	runtime.Goexit()
}

func (r *recordingT) Skipf(format string, args ...any) {
	r.mu.Lock()
	r.skipped = true
	r.logs = append(r.logs, fmt.Sprintf(format, args...))
	r.mu.Unlock()

	runtime.Goexit()
}

func (r *recordingT) Logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.logs = append(r.logs, fmt.Sprintf(format, args...))
}

func (r *recordingT) Context() context.Context { return r.ctx }

func (r *recordingT) TempDir() string {
	dir, err := os.MkdirTemp("", "invoketest-*")
	if err != nil {
		r.Fatalf("creating temp dir: %v", err)
	}

	r.mu.Lock()
	r.tempDirs = append(r.tempDirs, dir)
	r.mu.Unlock()

	return dir
}

func (r *recordingT) Name() string { return r.name }

func (r *recordingT) cleanup() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, dir := range r.tempDirs {
		_ = os.RemoveAll(dir)
	}
}

// recordedOutcome is the observable result of driving one contract.
type recordedOutcome struct {
	failed   bool
	skipped  bool
	panicked any
	timedOut bool
	logs     []string
}

// runRecorded executes one contract against environments from factory and
// records what it did, honoring the capability gate exactly as Verify
// does.
func runRecorded(t *testing.T, tc TestCase, factory Factory) recordedOutcome {
	t.Helper()

	rec := &recordingT{name: tc.ID(), ctx: t.Context()}
	defer rec.cleanup()

	done := make(chan any, 1)

	go func() {
		var panicked any

		defer func() { done <- panicked }()
		defer func() { panicked = recover() }()

		env := factory(rec)

		defer func() { _ = env.Close() }()

		if tc.Gate != nil {
			if run, _ := tc.Gate(env.Capabilities()); !run {
				rec.mu.Lock()
				rec.skipped = true
				rec.mu.Unlock()

				return
			}
		}

		tc.Run(rec, env)
	}()

	outcome := recordedOutcome{}

	select {
	case panicked := <-done:
		outcome.panicked = panicked
	case <-time.After(selfTestTimeout):
		outcome.timedOut = true
	}

	rec.mu.Lock()
	outcome.failed = rec.failed || outcome.timedOut || outcome.panicked != nil
	outcome.skipped = rec.skipped
	outcome.logs = append(outcome.logs, rec.logs...)
	rec.mu.Unlock()

	return outcome
}

// contractByID resolves a contract or fails the test, keeping the defect
// catalog honest about renames.
func contractByID(t *testing.T, id string) TestCase {
	t.Helper()

	for _, tc := range AllContracts() {
		if tc.ID() == id {
			return tc
		}
	}

	require.Failf(t, "defect catalog references an unknown contract", "unknown contract %q", id)

	return TestCase{}
}

//nolint:tparallel,paralleltest // Contracts run sequentially by design: they share the machine.
func TestContractsPassAgainstReference(t *testing.T) {
	t.Parallel()

	for _, tc := range AllContracts() {
		t.Run(tc.ID(), func(t *testing.T) {
			factory, cleanup := newReferenceFactory(defects{})
			defer cleanup()

			outcome := runRecorded(t, tc, factory)

			require.Nil(t, outcome.panicked, "contract panicked against the reference")
			require.Falsef(t, outcome.failed,
				"contract failed against the clean reference:\n%s", formatLogs(outcome.logs))

			// A gated skip is legitimate (the capability pair covers
			// it); anything else must have run.
			require.Falsef(t, outcome.skipped && tc.Gate == nil,
				"ungated contract skipped against the reference:\n%s", formatLogs(outcome.logs))
		})
	}
}

//nolint:tparallel,paralleltest // Contracts run sequentially by design: they share the machine.
func TestEveryContractCanFail(t *testing.T) {
	t.Parallel()

	covered := make(map[string]bool)

	for _, dc := range defectCatalog() {
		t.Run(dc.name, func(t *testing.T) {
			tc := contractByID(t, dc.contract)
			covered[dc.contract] = true

			factory, cleanup := newReferenceFactory(dc.defects)
			defer cleanup()

			outcome := runRecorded(t, tc, factory)

			require.Nilf(t, outcome.panicked,
				"contract %q panicked under defect %q; contracts must fail via assertions, not crashes",
				dc.contract, dc.name)

			require.Falsef(t, outcome.skipped,
				"contract %q skipped under defect %q; the defect must reach it", dc.contract, dc.name)

			require.Truef(t, outcome.failed,
				"contract %q PASSED under defect %q; a contract that cannot fail is not a test",
				dc.contract, dc.name)
		})
	}

	for _, tc := range AllContracts() {
		assert.Truef(t, covered[tc.ID()],
			"contract %q has no defect in the catalog proving it can fail", tc.ID())
	}
}

func formatLogs(logs []string) string {
	if len(logs) == 0 {
		return "(no output)"
	}

	var out strings.Builder
	for _, line := range logs {
		out.WriteString("  " + line + "\n")
	}

	return out.String()
}
