package invoketest

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

//nolint:funlen
func coreContracts() []TestCase {
	return []TestCase{
		{
			Category: CategoryCore,
			Name:     "run-success-exit0",
			Run: func(t T, env invoke.Environment) {
				exec := invoke.NewExecutor(env)
				result, err := exec.RunBuffered(t.Context(), env.TargetOS().ShellCommand("echo hello"))
				require.NoError(t, err)
				require.NotNil(t, result)

				assert.Equal(t, "hello", strings.TrimSpace(string(result.Stdout)))
				assert.Equal(t, 0, result.ExitCode)
			},
		},
		{
			Category: CategoryCore,
			Name:     "run-nonzero-exiterror",
			Run: func(t T, env invoke.Environment) {
				want := 7

				result, err := env.Run(t.Context(), env.TargetOS().ShellCommand(exitScript(want)))
				require.Error(t, err)
				require.NotNil(t, result)

				var exitErr *invoke.ExitError
				require.ErrorAs(t, err, &exitErr)
				assert.Equal(t, want, exitErr.ExitCode)
				assert.Equal(t, want, result.ExitCode)
			},
		},
		{
			Category: CategoryCore,
			Name:     "run-env-propagation",
			Run: func(t T, env invoke.Environment) {
				exec := invoke.NewExecutor(env)
				cmd := env.TargetOS().ShellCommand(printEnvScript(env.TargetOS(), "INVOKE_CONTRACT_ENV"))
				cmd.Env = append(cmd.Env, "INVOKE_CONTRACT_ENV=from-contract")

				result, err := exec.RunBuffered(t.Context(), cmd)
				require.NoError(t, err)
				require.NotNil(t, result)

				assert.Equal(t, "from-contract", strings.TrimSpace(string(result.Stdout)))
				assert.Equal(t, 0, result.ExitCode)
			},
		},
		{
			Category: CategoryCore,
			Name:     "run-working-directory",
			Run: func(t T, env invoke.Environment) {
				exec := invoke.NewExecutor(env)
				dir := workingDirForTarget(env.TargetOS())
				cmd := env.TargetOS().ShellCommand(printWorkingDirScript(env.TargetOS()))
				cmd.Dir = dir

				result, err := exec.RunBuffered(t.Context(), cmd)
				require.NoError(t, err)
				require.NotNil(t, result)

				assert.Contains(t, strings.TrimSpace(string(result.Stdout)), dir)
				assert.Equal(t, 0, result.ExitCode)
			},
		},
		{
			Category: CategoryCore,
			Name:     "start-wait-success",
			Run: func(t T, env invoke.Environment) {
				process, err := env.Start(t.Context(), env.TargetOS().ShellCommand("echo start-contract"))
				require.NoError(t, err)
				require.NotNil(t, process)

				defer func() {
					_ = process.Close()
				}()

				err = process.Wait()
				require.NoError(t, err)

				result := process.Result()
				require.NotNil(t, result)
				assert.Equal(t, 0, result.ExitCode)
				assert.GreaterOrEqual(t, result.Duration, time.Duration(0))
			},
		},
		{
			Category: CategoryCore,
			Name:     "start-wait-nonzero-exiterror",
			Run: func(t T, env invoke.Environment) {
				want := 9

				process, err := env.Start(t.Context(), env.TargetOS().ShellCommand(exitScript(want)))
				require.NoError(t, err)
				require.NotNil(t, process)

				defer func() {
					_ = process.Close()
				}()

				err = process.Wait()
				require.Error(t, err)

				var exitErr *invoke.ExitError
				require.True(t, errors.As(err, &exitErr))
				assert.Equal(t, want, exitErr.ExitCode)

				result := process.Result()
				require.NotNil(t, result)
				assert.Equal(t, want, result.ExitCode)
				assert.GreaterOrEqual(t, result.Duration, time.Duration(0))
			},
		},
	}
}

func exitScript(code int) string {
	return "exit " + strconv.Itoa(code)
}

func printEnvScript(targetOS invoke.TargetOS, name string) string {
	if targetOS == invoke.OSWindows {
		return "$env:" + name
	}

	return "printf %s \"$" + name + "\""
}

func printWorkingDirScript(targetOS invoke.TargetOS) string {
	if targetOS == invoke.OSWindows {
		return "(Get-Location).Path"
	}

	return "pwd"
}

func workingDirForTarget(targetOS invoke.TargetOS) string {
	if targetOS == invoke.OSWindows {
		return `C:\Windows\Temp`
	}

	return "/tmp"
}
