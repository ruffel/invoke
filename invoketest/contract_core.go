package invoketest

import (
	"bytes"
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
				result, err := exec.RunBuffered(t.Context(), invoke.ShellCommand("echo hello"))
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

				result, err := env.Run(t.Context(), invoke.ShellCommand(exitScript(want)))
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
				cmd := invoke.ShellCommand(printEnvScript("INVOKE_CONTRACT_ENV"))
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
				dir := workingDirForTarget()
				cmd := invoke.ShellCommand(printWorkingDirScript())
				cmd.Dir = dir

				result, err := exec.RunBuffered(t.Context(), cmd)
				require.NoError(t, err)
				require.NotNil(t, result)

				assert.Contains(t, strings.ToLower(strings.TrimSpace(string(result.Stdout))), strings.ToLower(dir))
				assert.Equal(t, 0, result.ExitCode)
			},
		},
		{
			Category: CategoryCore,
			Name:     "start-wait-success",
			Run: func(t T, env invoke.Environment) {
				process, err := env.Start(t.Context(), invoke.ShellCommand("echo start-contract"))
				require.NoError(t, err)
				require.NotNil(t, process)

				defer func() {
					_ = process.Close()
				}()

				result, err := process.Wait()
				require.NoError(t, err)
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

				process, err := env.Start(t.Context(), invoke.ShellCommand(exitScript(want)))
				require.NoError(t, err)
				require.NotNil(t, process)

				defer func() {
					_ = process.Close()
				}()

				result, err := process.Wait()
				require.Error(t, err)

				var exitErr *invoke.ExitError
				require.ErrorAs(t, err, &exitErr)
				assert.Equal(t, want, exitErr.ExitCode)

				require.NotNil(t, result)
				assert.Equal(t, want, result.ExitCode)
				assert.GreaterOrEqual(t, result.Duration, time.Duration(0))
			},
		},
		{
			Category:    CategoryCore,
			Name:        "wait-result-always-populated",
			Description: "Wait must return a non-nil *Result even when it returns an error",
			Run: func(t T, env invoke.Environment) {
				const wantCode = 42

				process, err := env.Start(t.Context(), invoke.ShellCommand(exitScript(wantCode)))
				require.NoError(t, err)
				require.NotNil(t, process)

				defer func() {
					_ = process.Close()
				}()

				result, waitErr := process.Wait()
				require.Error(t, waitErr, "Wait must return an error for non-zero exit")
				require.NotNil(t, result, "Result must be non-nil even when Wait returns an error")
				assert.Equal(t, wantCode, result.ExitCode)
				assert.GreaterOrEqual(t, result.Duration, time.Duration(0))
			},
		},
		{
			Category:    CategoryCore,
			Name:        "wait-idempotent",
			Description: "Calling Wait twice on the same process returns the same result without blocking",
			Run: func(t T, env invoke.Environment) {
				process, err := env.Start(t.Context(), invoke.ShellCommand("echo idempotent"))
				require.NoError(t, err)
				require.NotNil(t, process)

				defer func() {
					_ = process.Close()
				}()

				result1, err1 := process.Wait()
				result2, err2 := process.Wait()

				require.NoError(t, err1)
				require.NoError(t, err2)
				require.NotNil(t, result1)
				require.NotNil(t, result2)
				assert.Equal(t, result1.ExitCode, result2.ExitCode)
			},
		},
		{
			Category:    CategoryCore,
			Name:        "start-stdout-capture",
			Description: "Stdout written by a started process can be captured via Command.Stdout",
			Run: func(t T, env invoke.Environment) {
				var buf strings.Builder

				cmd := invoke.ShellCommand("echo from-start")
				cmd.Stdout = &buf

				process, err := env.Start(t.Context(), cmd)
				require.NoError(t, err)
				require.NotNil(t, process)

				defer func() {
					_ = process.Close()
				}()

				_, err = process.Wait()
				require.NoError(t, err)

				assert.Equal(t, "from-start", strings.TrimSpace(buf.String()))
			},
		},
		{
			Category:    CategoryCore,
			Name:        "start-stderr-capture",
			Description: "Stderr written by a started process can be captured via Command.Stderr",
			Run: func(t T, env invoke.Environment) {
				var buf bytes.Buffer

				cmd := invoke.ShellCommand("echo from-stderr >&2")
				cmd.Stderr = &buf

				process, err := env.Start(t.Context(), cmd)
				require.NoError(t, err)
				require.NotNil(t, process)

				defer func() {
					_ = process.Close()
				}()

				_, err = process.Wait()
				require.NoError(t, err)

				assert.Equal(t, "from-stderr", strings.TrimSpace(buf.String()))
			},
		},
		{
			Category:    CategoryCore,
			Name:        "run-direct-binary",
			Description: "Run a binary directly without sh -c, verifying the non-shell code path",
			Run: func(t T, env invoke.Environment) {
				exec := invoke.NewExecutor(env)

				result, err := exec.RunBuffered(t.Context(), &invoke.Command{
					Cmd:  "echo",
					Args: []string{"direct-binary"},
				})
				require.NoError(t, err)
				require.NotNil(t, result)

				assert.Equal(t, "direct-binary", strings.TrimSpace(string(result.Stdout)))
				assert.Equal(t, 0, result.ExitCode)
			},
		},
	}
}

func exitScript(code int) string {
	return "exit " + strconv.Itoa(code)
}

func printEnvScript(name string) string {
	return "printf %s \"$" + name + "\""
}

func printWorkingDirScript() string {
	return "pwd"
}

func workingDirForTarget() string {
	return "/tmp"
}
