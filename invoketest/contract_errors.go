package invoketest

import (
	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/require"
)

const (
	runExitErrorCode  = 13
	waitExitErrorCode = 23
)

func errorContracts() []TestCase {
	return []TestCase{
		runNilCommandReturnsErrorContract(),
		runEmptyCommandReturnsErrorContract(),
		startNilCommandReturnsErrorContract(),
		startEmptyCommandReturnsErrorContract(),
		runNonZeroReturnsExitErrorContract(),
		startWaitNonZeroReturnsExitErrorContract(),
		ttyUnsupportedNormalizedContract(),
	}
}

func runNilCommandReturnsErrorContract() TestCase {
	return TestCase{
		Category:    CategoryErrors,
		Name:        "run-nil-command-returns-error",
		Description: "Run with a nil command must return an error",
		Run: func(t T, env invoke.Environment) {
			_, err := env.Run(t.Context(), nil)
			require.Error(t, err)
		},
	}
}

func runEmptyCommandReturnsErrorContract() TestCase {
	return TestCase{
		Category:    CategoryErrors,
		Name:        "run-empty-command-returns-error",
		Description: "Run with an empty command binary must return an error",
		Run: func(t T, env invoke.Environment) {
			_, err := env.Run(t.Context(), &invoke.Command{})
			require.Error(t, err)
		},
	}
}

func startNilCommandReturnsErrorContract() TestCase {
	return TestCase{
		Category:    CategoryErrors,
		Name:        "start-nil-command-returns-error",
		Description: "Start with a nil command must return an error",
		Run: func(t T, env invoke.Environment) {
			_, err := env.Start(t.Context(), nil)
			require.Error(t, err)
		},
	}
}

func startEmptyCommandReturnsErrorContract() TestCase {
	return TestCase{
		Category:    CategoryErrors,
		Name:        "start-empty-command-returns-error",
		Description: "Start with an empty command binary must return an error",
		Run: func(t T, env invoke.Environment) {
			_, err := env.Start(t.Context(), &invoke.Command{})
			require.Error(t, err)
		},
	}
}

func runNonZeroReturnsExitErrorContract() TestCase {
	return TestCase{
		Category:    CategoryErrors,
		Name:        "run-nonzero-returns-exiterror",
		Description: "Run non-zero failures must return *invoke.ExitError",
		Run: func(t T, env invoke.Environment) {
			_, err := env.Run(t.Context(), env.TargetOS().ShellCommand(exitScript(runExitErrorCode)))
			require.Error(t, err)

			var exitErr *invoke.ExitError
			require.ErrorAs(t, err, &exitErr)
			require.Equal(t, runExitErrorCode, exitErr.ExitCode)
		},
	}
}

func startWaitNonZeroReturnsExitErrorContract() TestCase {
	return TestCase{
		Category:    CategoryErrors,
		Name:        "start-wait-nonzero-returns-exiterror",
		Description: "Wait non-zero failures must return *invoke.ExitError",
		Run: func(t T, env invoke.Environment) {
			process, err := env.Start(t.Context(), env.TargetOS().ShellCommand(exitScript(waitExitErrorCode)))
			require.NoError(t, err)
			require.NotNil(t, process)

			defer func() {
				_ = process.Close()
			}()

			err = process.Wait()
			require.Error(t, err)

			var exitErr *invoke.ExitError
			require.ErrorAs(t, err, &exitErr)
			require.Equal(t, waitExitErrorCode, exitErr.ExitCode)
		},
	}
}

func ttyUnsupportedNormalizedContract() TestCase {
	return TestCase{
		Category:    CategoryErrors,
		Name:        "tty-unsupported-normalized",
		Description: "If TTY is unsupported by a provider, it must wrap invoke.ErrNotSupported",
		Run: func(t T, env invoke.Environment) {
			cmd := env.TargetOS().ShellCommand("echo invoke-contract-tty")
			cmd.Tty = true

			process, err := env.Start(t.Context(), cmd)
			if err != nil {
				require.ErrorIs(t, err, invoke.ErrNotSupported)

				return
			}

			require.NotNil(t, process)

			defer func() {
				_ = process.Close()
			}()

			require.NoError(t, process.Wait())
		},
	}
}
