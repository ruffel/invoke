package invoketest

import (
	"context"
	"time"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// terminationGrace is how long the termination contract waits after
// cancellation before checking that the marker command never ran. It must
// comfortably exceed the marker script's own sleep.
const terminationGrace = 2 * time.Second

func lifecycleContracts() []TestCase {
	return []TestCase{
		lifecycleWaitIsIdempotent(),
		lifecycleConcurrentWaitIsSafe(),
		lifecycleCancelUnblocksWait(),
		lifecycleBlockedStdinCannotHangWait(),
		lifecycleDeadlineUnblocksWait(),
		lifecycleCancelTerminatesProcess(),
		lifecycleCancelAfterExitKeepsOutcome(),
		lifecycleCancelDuringDrainKeepsOutcome(),
		lifecycleStartOnCanceledContext(),
		lifecycleConcurrentProcessesRun(),
		lifecycleCloseUnblocksWait(),
		lifecycleCloseIsIdempotent(),
		lifecycleCloseAfterWaitKeepsOutcome(),
		lifecycleEnvCloseTerminatesProcesses(),
		lifecycleSignalTerminatesProcess(),
		lifecycleSignalAttributionRoundTrips(),
		lifecycleSignalAfterExitErrors(),
		lifecycleUnsupportedSignalNormalized(),
	}
}

func lifecycleConcurrentWaitIsSafe() TestCase {
	return TestCase{
		Category:    CategoryLifecycle,
		Name:        "concurrent-wait-is-safe",
		Description: "Concurrent Wait callers all observe the same outcome without racing or deadlocking",
		Run: func(t T, env invoke.Environment) {
			proc := startCommand(t.Context(), t, env, invoke.Shell("exit 7"), invoke.IO{})

			const callers = 4

			results := make(chan waitOutcome, callers)

			for range callers {
				go func() {
					res, err := proc.Wait()
					results <- waitOutcome{result: res, err: err}
				}()
			}

			var first waitOutcome

			for i := range callers {
				select {
				case got := <-results:
					if i == 0 {
						first = got

						continue
					}

					assert.Equal(t, first.result, got.result,
						"concurrent Wait callers must all observe the same outcome")
				case <-time.After(contractTimeout):
					require.Failf(t, "concurrent Wait callers blocked past the contract deadline",
						"not all callers returned within %v", contractTimeout)
				}
			}
		},
	}
}

func lifecycleDeadlineUnblocksWait() TestCase {
	return TestCase{
		Category:    CategoryLifecycle,
		Name:        "deadline-unblocks-wait",
		Description: "A context deadline unblocks Wait with an error matching DeadlineExceeded, distinct from a plain cancel",
		Run: func(t T, env invoke.Environment) {
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()

			proc := startCommand(ctx, t, env, invoke.New("sleep", "30"), invoke.IO{})

			defer func() { _ = proc.Close() }()

			outcome := waitOrTimeout(t, proc)
			require.Error(t, outcome.err, "Wait after deadline returned nil error")

			assert.ErrorIs(t, outcome.err, context.DeadlineExceeded,
				"a deadline must stay distinguishable from a plain cancel")

			requireNotExitError(t, outcome.err, "deadline expiry")
		},
	}
}

func lifecycleCancelAfterExitKeepsOutcome() TestCase {
	return TestCase{
		Category:    CategoryLifecycle,
		Name:        "cancel-after-exit-keeps-outcome",
		Description: "Cancellation observed after a process already exited must not rewrite its real outcome",
		Run: func(t T, env invoke.Environment) {
			ctx, cancel := context.WithCancel(t.Context())

			proc := startCommand(ctx, t, env, invoke.New("true"), invoke.IO{})

			// Let the process finish on its own, then cancel before Wait
			// reads the outcome: the real exit-zero must win over the
			// late cancellation.
			outcome := waitOrTimeout(t, proc)

			cancel()

			// Re-read: a provider that lazily consults ctx.Err() at Wait
			// time would corrupt the cached success here.
			second := waitOrTimeout(t, proc)

			assert.NoError(t, outcome.err,
				"the process exited 0 before cancellation; the real outcome must win")
			assert.NoError(t, second.err,
				"the process exited 0 before cancellation; a late cancel must not rewrite it")

			assert.Equal(t, 0, second.result.ExitCode, "a late cancel must not rewrite the exit code")
		},
	}
}

// lifecycleCancelDuringDrainKeepsOutcome pins the outcome of a process
// that finished while its output was still being collected.
//
// [lifecycleCancelAfterExitKeepsOutcome] reads the outcome before it
// cancels, so a provider that decides the outcome once and remembers it
// passes without the question ever being asked. The question is what a
// provider decides when the cancellation arrives first: the process has
// already exited, but Wait has not yet worked out what to report. Reading
// the context at that moment rather than the process's own status turns a
// success into a cancellation, and a caller who retries on cancellation
// runs a command that already ran.
//
// The contract holds the output open to make that moment last. Stdout is
// a writer that blocks, so the provider is still draining when the
// context is canceled, and the process it is draining for is long gone.
func lifecycleCancelDuringDrainKeepsOutcome() TestCase {
	return TestCase{
		Category:    CategoryLifecycle,
		Name:        "cancel-during-drain-keeps-outcome",
		Description: "Cancellation arriving while output is still draining must not rewrite the exit of a process that already finished",
		Run: func(t T, env invoke.Environment) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			drain := newBlockingWriter()

			// The script's last act is the write, so once the bytes arrive
			// the process is on its way out.
			proc := startCommand(ctx, t, env, invoke.Shell("echo draining"), invoke.IO{Stdout: drain})

			defer func() { _ = proc.Close() }()

			select {
			case <-drain.started:
			case <-time.After(contractTimeout):
				require.FailNow(t, "the command produced no output within the contract deadline")
			}

			// The write has landed and the drain is held, so the provider
			// cannot finish Wait while the process finishes exiting.
			time.Sleep(exitSettle)

			cancel()

			// Still held for a moment after the cancel, so a provider that
			// consults the context on its way out has every chance to.
			time.Sleep(exitSettle)

			drain.release()

			outcome := waitOrTimeout(t, proc)

			assert.NoError(t, outcome.err,
				"the process exited 0 before the cancellation; draining its output must not rewrite that")

			assert.Equal(t, 0, outcome.result.ExitCode,
				"the process exited 0 before the cancellation; draining its output must not rewrite its exit code")
		},
	}
}

func lifecycleConcurrentProcessesRun() TestCase {
	return TestCase{
		Category:    CategoryLifecycle,
		Name:        "concurrent-processes-run",
		Description: "One environment runs multiple processes simultaneously, each with an independent outcome",
		Run: func(t T, env invoke.Environment) {
			const codeA, codeB = 3, 4

			procA := startCommand(t.Context(), t, env, invoke.Shell("exit 3"), invoke.IO{})
			procB := startCommand(t.Context(), t, env, invoke.Shell("exit 4"), invoke.IO{})

			// Both are alive at once (neither has been waited yet);
			// their outcomes must not interfere.
			outcomeA := waitOrTimeout(t, procA)
			outcomeB := waitOrTimeout(t, procB)

			assert.Equal(t, codeA, requireExitError(t, outcomeA.err).Code,
				"process A's outcome must be independent")
			assert.Equal(t, codeB, requireExitError(t, outcomeB.err).Code,
				"process B's outcome must be independent")
		},
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

			assert.Equal(t, first.result, second.result, "repeated Wait must return the same outcome")

			firstExit := requireExitError(t, first.err)
			secondExit := requireExitError(t, second.err)

			assert.Equal(t, firstExit.Code, secondExit.Code, "repeated Wait must report the same exit code")
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
			require.Error(t, outcome.err, "Wait after cancel returned nil error")

			assert.ErrorIs(t, outcome.err, context.Canceled)

			requireNotExitError(t, outcome.err, "cancellation")
		},
	}
}

// lifecycleBlockedStdinCannotHangWait pins the wedge no provider can
// interrupt: a caller-supplied Stdin whose Read never returns — an
// io.Pipe nobody writes to, a network stream gone quiet.
//
// Nothing in Go can unblock that read, so the temptation is to wait for
// it: a provider whose Wait joins the goroutine pumping the caller's
// reader holds the caller hostage to a reader that owes it nothing,
// long after the process is dead. The reader is the caller's property;
// the process's end is the provider's. This contract keeps the two
// apart.
func lifecycleBlockedStdinCannotHangWait() TestCase {
	return TestCase{
		Category:    CategoryLifecycle,
		Name:        "blocked-stdin-cannot-hang-wait",
		Description: "A caller Stdin whose Read never returns cannot hold Wait or Close hostage once the process is told to stop",
		Run: func(t T, env invoke.Environment) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			stdin := newBlockingReader()
			defer stdin.release()

			proc := startCommand(ctx, t, env, invoke.New("cat"), invoke.IO{Stdin: stdin})

			defer func() { _ = proc.Close() }()

			// The wedge must be real before it is tested: something —
			// the provider's pump or the command itself — is now inside
			// the caller's Read.
			select {
			case <-stdin.started:
			case <-time.After(contractTimeout):
				require.FailNow(t, "nothing read from the caller's stdin within the contract deadline")
			}

			cancel()

			outcome := waitOrTimeout(t, proc)
			require.Error(t, outcome.err, "Wait after cancel returned nil error")

			assert.ErrorIs(t, outcome.err, context.Canceled,
				"the interruption must be attributed to the caller's cancel, wedged stdin or not")

			requireNotExitError(t, outcome.err, "cancellation with a wedged stdin")

			assert.NoError(t, closeOrTimeout(t, proc), "Close after an abandoned stdin read")
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
			require.Error(t, outcome.err, "Wait after cancel returned nil error")

			// Give a surviving process ample time to prove itself,
			// then check through the environment's own shell.
			time.Sleep(terminationGrace)

			assert.Truef(t, targetProbe(t, env, "test ! -e "+shellQuote(marker)),
				"marker %q exists on the target; cancellation did not terminate the process", marker)
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

				require.Fail(t, "Start on a canceled context succeeded")
			}

			assert.ErrorIs(t, err, context.Canceled)
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

			assert.NoError(t, closeOrTimeout(t, proc), "Close of a running process")

			outcome := waitOrTimeout(t, proc)
			require.Error(t, outcome.err, "Wait after Close returned nil error")

			assert.ErrorIs(t, outcome.err, invoke.ErrClosed, "Wait after Close")

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

			assert.NoError(t, closeOrTimeout(t, proc), "first Close")
			assert.NoError(t, closeOrTimeout(t, proc), "second Close")
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
			require.NoError(t, first.err, "Wait must succeed")

			assert.NoError(t, closeOrTimeout(t, proc), "Close after Wait")

			second := waitOrTimeout(t, proc)
			assert.NoError(t, second.err, "Close must not invalidate the cached outcome")
			assert.Equal(t, first.result, second.result, "Close must not invalidate the cached outcome")
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

			require.NoError(t, env.Close(), "Environment.Close")

			outcome := waitOrTimeout(t, proc)
			require.Error(t, outcome.err, "Wait after environment Close returned nil error")

			assert.ErrorIs(t, outcome.err, invoke.ErrClosed, "Wait after environment Close")

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

			require.NoError(t, proc.Signal(invoke.SIGTERM),
				"Signal(TERM) must succeed on a target declaring signal delivery")

			outcome := waitOrTimeout(t, proc)

			exitErr := requireExitError(t, outcome.err)
			assert.Equal(t, invoke.SIGTERM, exitErr.Signal)
			assert.Equal(t, -1, exitErr.Code, "a signal death reports Code -1")
		},
	}
}

func lifecycleSignalAttributionRoundTrips() TestCase {
	return TestCase{
		Category:    CategoryLifecycle,
		Name:        "signal-attribution-round-trips",
		Description: "Each supported signal name is delivered faithfully and reported back under the same name",
		Gate: func(caps invoke.Capabilities) (bool, string) {
			return caps.Signals, "target does not declare signal delivery; lifecycle/unsupported-signal-normalized covers it"
		},
		Run: func(t T, env invoke.Environment) {
			// A provider whose name->wire mapping is wrong for any of
			// these delivers the wrong signal (or reports the wrong name
			// back); the kernel's own attribution catches both, since
			// each of these signals default-terminates an untrapped
			// process. SIGTERM is covered by signal-terminates-process.
			for _, sig := range []invoke.Signal{invoke.SIGINT, invoke.SIGQUIT, invoke.SIGUSR1, invoke.SIGUSR2} {
				proc := startCommand(t.Context(), t, env, invoke.New("sleep", "30"), invoke.IO{})

				if err := proc.Signal(sig); err != nil {
					_ = proc.Close()

					require.NoErrorf(t, err, "Signal(%s) must succeed on a target declaring signal delivery", sig)
				}

				outcome := waitOrTimeout(t, proc)

				exitErr := requireExitError(t, outcome.err)
				assert.Equalf(t, sig, exitErr.Signal,
					"sent %s but Wait reports a different signal; the name mapping is not faithful", sig)
				assert.Equalf(t, -1, exitErr.Code, "a %s death reports Code -1", sig)
			}
		},
	}
}

func lifecycleSignalAfterExitErrors() TestCase {
	return TestCase{
		Category:    CategoryLifecycle,
		Name:        "signal-after-exit-errors",
		Description: "Signaling a process that has already exited returns an error, never a silent success",
		Gate: func(caps invoke.Capabilities) (bool, string) {
			return caps.Signals, "target does not declare signal delivery"
		},
		Run: func(t T, env invoke.Environment) {
			proc := startCommand(t.Context(), t, env, invoke.New("true"), invoke.IO{})

			require.NoError(t, waitOrTimeout(t, proc).err, "Wait must succeed")

			assert.Error(t, proc.Signal(invoke.SIGTERM),
				"signaling a gone process must report an error, not silently succeed")
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
			require.Errorf(t, err,
				"Signal(%s) on a target that cannot deliver it; silent no-ops are forbidden", sig)

			assert.ErrorIs(t, err, invoke.ErrNotSupported)
		},
	}
}
