package invoketest

import (
	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func errorsContracts() []TestCase {
	return []TestCase{
		errorsMissingBinaryNotFound(),
		errorsShellMissingBinaryIsExit127(),
		errorsBadWorkdirClassified(),
		errorsLookPathClassifies(),
		errorsClosedEnvRefusesAll(),
		errorsTerminalOutcomesAreNotTransport(),
	}
}

func errorsShellMissingBinaryIsExit127() TestCase {
	return TestCase{
		Category:    CategoryErrors,
		Name:        "shell-missing-binary-is-exit-127",
		Description: "A missing binary inside a shell command is the shell's exit 127, not a start-time ErrNotFound",
		Run: func(t T, env invoke.Environment) {
			// The command that runs is the shell, which exists; the
			// missing binary is the shell's own runtime failure, so this
			// must be an ExitError(127), never conflated with the
			// start-time not-found classification.
			outcome, _, _ := runCapture(t, env,
				invoke.Shell("invoke-missing-"+token(t)))

			require.NotErrorIs(t, outcome.err, invoke.ErrNotFound,
				"a missing binary inside a shell command must not surface as ErrNotFound; the shell ran and exited")

			const wantCode = 127

			exitErr := requireExitError(t, outcome.err)
			assert.Equal(t, wantCode, exitErr.Code, "the shell's own command-not-found status")
		},
	}
}

func errorsMissingBinaryNotFound() TestCase {
	return TestCase{
		Category:    CategoryErrors,
		Name:        "missing-binary-not-found",
		Description: "A binary the target cannot resolve fails wrapping ErrNotFound",
		Run: func(t T, env invoke.Environment) {
			proc, err := env.Start(t.Context(),
				invoke.New("invoke-definitely-missing-"+token(t)), invoke.IO{})
			if err == nil {
				if proc != nil {
					_ = proc.Close()
				}

				require.Fail(t, "starting a nonexistent binary succeeded")
			}

			assert.ErrorIs(t, err, invoke.ErrNotFound)
		},
	}
}

func errorsBadWorkdirClassified() TestCase {
	return TestCase{
		Category:    CategoryErrors,
		Name:        "bad-workdir-classified",
		Description: "A nonexistent working directory fails wrapping ErrInvalidWorkdir, not as a command outcome",
		Run: func(t T, env invoke.Environment) {
			cmd := invoke.New("true")
			cmd.Dir = "/tmp/invoke-no-such-dir-" + token(t)

			proc, err := env.Start(t.Context(), cmd, invoke.IO{})
			if err == nil {
				if proc != nil {
					_, _ = proc.Wait()
				}

				require.Fail(t, "starting in a nonexistent workdir reported no error")
			}

			assert.ErrorIs(t, err, invoke.ErrInvalidWorkdir)

			requireNotExitError(t, err, "a workdir setup failure")
		},
	}
}

func errorsLookPathClassifies() TestCase {
	return TestCase{
		Category:    CategoryErrors,
		Name:        "lookpath-classifies",
		Description: "LookPath resolves real names to a path (not a bare name) and wraps ErrNotFound for unresolvable ones",
		Run: func(t T, env invoke.Environment) {
			path, err := env.LookPath(t.Context(), "sh")
			require.NoError(t, err, "LookPath(sh) must resolve a real name")
			require.NotEmpty(t, path, "LookPath(sh) must return a resolved path")

			// A resolved executable is a path, not a bare command name:
			// callers use it as something they can exec directly.
			assert.Contains(t, path, "/",
				"LookPath must return a path containing a separator, not a bare name")

			_, err = env.LookPath(t.Context(), "invoke-definitely-missing-"+token(t))
			assert.ErrorIs(t, err, invoke.ErrNotFound, "LookPath of an unresolvable name")
		},
	}
}

func errorsTerminalOutcomesAreNotTransport() TestCase {
	return TestCase{
		Category: CategoryErrors,
		Name:     "terminal-outcomes-are-not-transport",
		Description: "An outcome that forecloses retrying — a command that ran, or one that could not " +
			"be started for a settled reason — must never classify as a retryable TransportError",
		Run: func(t T, env invoke.Environment) {
			ctx := t.Context()

			// The command ran and failed. Retrying it could run it again,
			// so a non-zero exit is terminal however it is reported.
			exit, _, _ := runCapture(t, env, invoke.Shell("exit 3"))
			requireNotTransport(t, exit.err, "a non-zero exit")

			// Could not be started, for reasons asking again will not
			// change: the binary is not there, the directory is not there.
			_, notFound := env.Start(ctx, invoke.New("invoke-missing-"+token(t)), invoke.IO{})
			requireNotTransport(t, notFound, "a missing binary")

			badDir := invoke.New("true")
			badDir.Dir = "/invoke-no-such-dir-" + token(t)

			_, badWorkdir := env.Start(ctx, badDir, invoke.IO{})
			requireNotTransport(t, badWorkdir, "an invalid working directory")

			// A feature the target declares it cannot provide is settled
			// too — only targets that lack it can be asked to prove this.
			if !env.Capabilities().TTY {
				_, unsupported := env.Start(ctx, invoke.New("true"), invoke.IO{TTY: &invoke.TTY{}})
				requireNotTransport(t, unsupported, "an unsupported TTY request")
			}

			// After Close, every call is terminal.
			require.NoError(t, env.Close(), "closing the environment")

			_, closed := env.Start(ctx, invoke.New("true"), invoke.IO{})
			requireNotTransport(t, closed, "a closed environment")
		},
	}
}

func errorsClosedEnvRefusesAll() TestCase {
	return TestCase{
		Category:    CategoryErrors,
		Name:        "closed-env-refuses-all",
		Description: "After Close, every method fails wrapping ErrClosed",
		Run: func(t T, env invoke.Environment) {
			require.NoError(t, env.Close())

			ctx := t.Context()

			_, startErr := env.Start(ctx, invoke.New("true"), invoke.IO{})
			assert.ErrorIs(t, startErr, invoke.ErrClosed, "Start after Close")

			_, lookErr := env.LookPath(ctx, "sh")
			assert.ErrorIs(t, lookErr, invoke.ErrClosed, "LookPath after Close")

			assert.ErrorIs(t, env.Upload(ctx, "src", "dst"), invoke.ErrClosed, "Upload after Close")
			assert.ErrorIs(t, env.Download(ctx, "src", "dst"), invoke.ErrClosed, "Download after Close")
		},
	}
}
