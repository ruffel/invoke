package invoketest

import (
	"os"
	"path/filepath"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/require"
)

const (
	runExitErrorCode   = 13
	waitExitErrorCode  = 23
	unsupportedOwnerID = 12345
	unsupportedGroupID = 23456
)

func errorContracts() []TestCase {
	return []TestCase{
		runNonZeroReturnsExitErrorContract(),
		startWaitNonZeroReturnsExitErrorContract(),
		ttyUnsupportedNormalizedContract(),
		uploadOwnerUnsupportedNormalizedContract(),
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

func uploadOwnerUnsupportedNormalizedContract() TestCase {
	return TestCase{
		Category:    CategoryErrors,
		Name:        "upload-owner-unsupported-normalized",
		Description: "If owner options are unsupported, Upload must wrap invoke.ErrNotSupported",
		Run: func(t T, env invoke.Environment) {
			srcPath := filepath.Join(t.TempDir(), "owner-src.txt")
			require.NoError(t, os.WriteFile(srcPath, []byte("owner-option-test"), 0o644))

			dstBase, _ := getTestPaths(t, env)
			dstPath := joinRemote(env, dstBase, "owner-dst.txt")

			err := env.Upload(
				t.Context(),
				srcPath,
				dstPath,
				invoke.WithOwner(unsupportedOwnerID, unsupportedGroupID),
			)
			if err == nil {
				return
			}

			require.ErrorIs(t, err, invoke.ErrNotSupported)
		},
	}
}
