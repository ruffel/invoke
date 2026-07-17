package invoketest

import (
	"context"
	"errors"
	"time"

	"github.com/ruffel/invoke"
)

// terminationGrace is how long the termination contract waits after
// cancellation before checking that the marker command never ran. It must
// comfortably exceed the marker script's own sleep.
const terminationGrace = 2 * time.Second

func lifecycleContracts() []TestCase {
	return []TestCase{
		lifecycleWaitIsIdempotent(),
		lifecycleCancelUnblocksWait(),
		lifecycleCancelTerminatesProcess(),
		lifecycleStartOnCanceledContext(),
		lifecycleCloseUnblocksWait(),
		lifecycleCloseIsIdempotent(),
		lifecycleCloseAfterWaitKeepsOutcome(),
		lifecycleEnvCloseTerminatesProcesses(),
		lifecycleSignalTerminatesProcess(),
		lifecycleUnsupportedSignalNormalized(),
	}
}

func lifecycleWaitIsIdempotent() TestCase {
	return TestCase{
		Category:    CategoryLifecycle,
		Name:        "wait-is-idempotent",
		Description: "Repeated Wait calls return the same outcome without blocking again",
		Run: func(t T, env invoke.Environment) {
			proc := startCommand(t.Context(), t, env, invoke.Shell("exit 19"), invoke.IO{})

			first := waitOrTimeout(t, proc)
			second := waitOrTimeout(t, proc)

			if first.result != second.result {
				t.Errorf("repeated Wait results differ: %+v vs %+v", first.result, second.result)
			}

			firstExit := requireExitError(t, first.err)
			secondExit := requireExitError(t, second.err)

			if firstExit.Code != secondExit.Code {
				t.Errorf("repeated Wait exit codes differ: %d vs %d", firstExit.Code, secondExit.Code)
			}
		},
	}
}

func lifecycleCancelUnblocksWait() TestCase {
	return TestCase{
		Category:    CategoryLifecycle,
		Name:        "cancel-unblocks-wait",
		Description: "Canceling the start context unblocks Wait promptly with an error matching ctx.Err(), never an ExitError",
		Run: func(t T, env invoke.Environment) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			proc := startCommand(ctx, t, env, invoke.New("sleep", "30"), invoke.IO{})

			defer func() { _ = proc.Close() }()

			cancel()

			outcome := waitOrTimeout(t, proc)
			if outcome.err == nil {
				failf(t, "Wait after cancel returned nil error")
			}

			if !errors.Is(outcome.err, context.Canceled) {
				t.Errorf("Wait after cancel = %v, want an error matching context.Canceled", outcome.err)
			}

			requireNotExitError(t, outcome.err, "cancellation")
		},
	}
}

func lifecycleCancelTerminatesProcess() TestCase {
	return TestCase{
		Category:    CategoryLifecycle,
		Name:        "cancel-terminates-process",
		Description: "Canceling the context terminates the process on the target, not merely the caller's Wait",
		Run: func(t T, env invoke.Environment) {
			marker := "/tmp/invoke-cancel-" + token(t)
			defer cleanupTargetPath(t, env, marker)

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			// The command creates the marker after a short sleep; a
			// real kill means the marker never appears.
			proc := startCommand(ctx, t, env,
				invoke.Shell("sleep 0.5 && touch "+shellQuote(marker)), invoke.IO{})

			defer func() { _ = proc.Close() }()

			cancel()

			outcome := waitOrTimeout(t, proc)
			if outcome.err == nil {
				failf(t, "Wait after cancel returned nil error")
			}

			// Give a surviving process ample time to prove itself,
			// then check through the environment's own shell.
			time.Sleep(terminationGrace)

			if !targetProbe(t, env, "test ! -e "+shellQuote(marker)) {
				t.Errorf("marker %q exists on the target; cancellation did not terminate the process", marker)
			}
		},
	}
}

func lifecycleStartOnCanceledContext() TestCase {
	return TestCase{
		Category:    CategoryLifecycle,
		Name:        "start-on-canceled-context",
		Description: "Starting on an already-canceled context fails with an error matching ctx.Err()",
		Run: func(t T, env invoke.Environment) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			proc, err := env.Start(ctx, invoke.New("sleep", "30"), invoke.IO{})
			if err == nil {
				if proc != nil {
					_ = proc.Close()
				}

				failf(t, "Start on a canceled context succeeded")
			}

			if !errors.Is(err, context.Canceled) {
				t.Errorf("Start on canceled context = %v, want an error matching context.Canceled", err)
			}
		},
	}
}

func lifecycleCloseUnblocksWait() TestCase {
	return TestCase{
		Category:    CategoryLifecycle,
		Name:        "close-unblocks-wait",
		Description: "Close terminates a running process promptly; Wait reports ErrClosed, never an ExitError",
		Run: func(t T, env invoke.Environment) {
			proc := startCommand(t.Context(), t, env, invoke.New("sleep", "30"), invoke.IO{})

			if err := closeOrTimeout(t, proc); err != nil {
				t.Errorf("Close = %v, want nil", err)
			}

			outcome := waitOrTimeout(t, proc)
			if outcome.err == nil {
				failf(t, "Wait after Close returned nil error")
			}

			if !errors.Is(outcome.err, invoke.ErrClosed) {
				t.Errorf("Wait after Close = %v, want an error wrapping ErrClosed", outcome.err)
			}

			requireNotExitError(t, outcome.err, "Close")
		},
	}
}

func lifecycleCloseIsIdempotent() TestCase {
	return TestCase{
		Category:    CategoryLifecycle,
		Name:        "close-is-idempotent",
		Description: "Closing a process twice is deterministic and error-free",
		Run: func(t T, env invoke.Environment) {
			proc := startCommand(t.Context(), t, env, invoke.New("sleep", "30"), invoke.IO{})

			if err := closeOrTimeout(t, proc); err != nil {
				t.Errorf("first Close = %v, want nil", err)
			}

			if err := closeOrTimeout(t, proc); err != nil {
				t.Errorf("second Close = %v, want nil", err)
			}
		},
	}
}

func lifecycleCloseAfterWaitKeepsOutcome() TestCase {
	return TestCase{
		Category:    CategoryLifecycle,
		Name:        "close-after-wait-keeps-outcome",
		Description: "Close after a completed Wait does not invalidate the cached outcome",
		Run: func(t T, env invoke.Environment) {
			proc := startCommand(t.Context(), t, env, invoke.New("true"), invoke.IO{})

			first := waitOrTimeout(t, proc)
			if first.err != nil {
				failf(t, "Wait = %v, want success", first.err)
			}

			if err := closeOrTimeout(t, proc); err != nil {
				t.Errorf("Close after Wait = %v, want nil", err)
			}

			second := waitOrTimeout(t, proc)
			if second.err != nil || second.result != first.result {
				t.Errorf("Wait after Close changed the outcome: (%+v, %v) vs (%+v, %v)",
					second.result, second.err, first.result, first.err)
			}
		},
	}
}

func lifecycleEnvCloseTerminatesProcesses() TestCase {
	return TestCase{
		Category:    CategoryLifecycle,
		Name:        "env-close-terminates-processes",
		Description: "Closing the environment terminates processes still running under it",
		Run: func(t T, env invoke.Environment) {
			proc := startCommand(t.Context(), t, env, invoke.New("sleep", "30"), invoke.IO{})

			if err := env.Close(); err != nil {
				failf(t, "Environment.Close = %v", err)
			}

			outcome := waitOrTimeout(t, proc)
			if outcome.err == nil {
				failf(t, "Wait after environment Close returned nil error")
			}

			if !errors.Is(outcome.err, invoke.ErrClosed) {
				t.Errorf("Wait after environment Close = %v, want an error wrapping ErrClosed", outcome.err)
			}

			requireNotExitError(t, outcome.err, "environment Close")
		},
	}
}

func lifecycleSignalTerminatesProcess() TestCase {
	return TestCase{
		Category:    CategoryLifecycle,
		Name:        "signal-terminates-process",
		Description: "A termination signal actually reaches the process: Wait unblocks promptly with the signal recorded",
		Gate: func(caps invoke.Capabilities) (bool, string) {
			return caps.Signals, "target does not declare signal delivery; lifecycle/unsupported-signal-normalized covers it"
		},
		Run: func(t T, env invoke.Environment) {
			proc := startCommand(t.Context(), t, env, invoke.New("sleep", "30"), invoke.IO{})

			defer func() { _ = proc.Close() }()

			if err := proc.Signal(invoke.SIGTERM); err != nil {
				failf(t, "Signal(TERM) = %v, want nil on a target declaring signal delivery", err)
			}

			outcome := waitOrTimeout(t, proc)

			exitErr := requireExitError(t, outcome.err)
			if exitErr.Signal != invoke.SIGTERM {
				t.Errorf("ExitError.Signal = %q, want TERM", exitErr.Signal)
			}

			if exitErr.Code != -1 {
				t.Errorf("ExitError.Code = %d for a signal death, want -1", exitErr.Code)
			}
		},
	}
}

func lifecycleUnsupportedSignalNormalized() TestCase {
	return TestCase{
		Category:    CategoryLifecycle,
		Name:        "unsupported-signal-normalized",
		Description: "An undeliverable signal returns an error wrapping ErrNotSupported — never a silent no-op",
		Run: func(t T, env invoke.Environment) {
			proc := startCommand(t.Context(), t, env, invoke.New("sleep", "30"), invoke.IO{})

			defer func() { _ = proc.Close() }()

			// On targets declaring signal delivery, probe with a name
			// outside the supported set; on targets without it, any
			// signal must be refused.
			sig := invoke.Signal("WINCH")
			if !env.Capabilities().Signals {
				sig = invoke.SIGTERM
			}

			err := proc.Signal(sig)
			if err == nil {
				failf(t, "Signal(%s) = nil on a target that cannot deliver it; silent no-ops are forbidden", sig)
			}

			if !errors.Is(err, invoke.ErrNotSupported) {
				t.Errorf("Signal(%s) = %v, want an error wrapping ErrNotSupported", sig, err)
			}
		},
	}
}
