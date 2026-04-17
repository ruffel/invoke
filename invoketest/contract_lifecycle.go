package invoketest

import (
	"context"
	"os"
	"time"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/require"
)

const lifecycleTimeout = 5 * time.Second

//nolint:funlen // Each test is necessarily self-contained and descriptive, so longer function length is acceptable here.
func lifecycleContracts() []TestCase {
	return []TestCase{
		{
			Category:    CategoryLifecycle,
			Name:        "environment-supports-sequential-runs",
			Description: "A single environment can be reused for multiple sequential commands",
			Run: func(t T, env invoke.Environment) {
				_, err := env.Run(t.Context(), invoke.ShellCommand("true"))
				require.NoError(t, err)

				_, err = env.Run(t.Context(), invoke.ShellCommand("true"))
				require.NoError(t, err)
			},
		},
		{
			Category:    CategoryLifecycle,
			Name:        "environment-allows-multiple-started-processes",
			Description: "A single environment can have more than one started process before waiting",
			Run: func(t T, env invoke.Environment) {
				process1, err := env.Start(t.Context(), invoke.ShellCommand("sleep 30"))
				require.NoError(t, err)
				require.NotNil(t, process1)

				process2, err := env.Start(t.Context(), invoke.ShellCommand("sleep 30"))
				require.NoError(t, err)
				require.NotNil(t, process2)

				require.NoError(t, process1.Close())
				require.NoError(t, process2.Close())
			},
		},
		{
			Category:    CategoryLifecycle,
			Name:        "process-close-idempotent",
			Description: "Closing a started process multiple times is deterministic and non-fatal",
			Run: func(t T, env invoke.Environment) {
				process, err := env.Start(t.Context(), invoke.ShellCommand("sleep 30"))
				require.NoError(t, err)
				require.NotNil(t, process)

				require.NoError(t, process.Close())
				require.NoError(t, process.Close())
			},
		},
		{
			Category:    CategoryLifecycle,
			Name:        "close-running-process-returns-promptly",
			Description: "Closing a running process should return in bounded time",
			Run: func(t T, env invoke.Environment) {
				process, err := env.Start(t.Context(), invoke.ShellCommand("sleep 30"))
				require.NoError(t, err)
				require.NotNil(t, process)

				done := make(chan error, 1)

				go func() {
					done <- process.Close()
				}()

				select {
				case err := <-done:
					require.NoError(t, err)
				case <-time.After(lifecycleTimeout):
					t.FailNow()
				}
			},
		},
		{
			Category:    CategoryLifecycle,
			Name:        "cancel-context-unblocks-wait",
			Description: "Canceling the start context should cause Wait to return in bounded time",
			Run: func(t T, env invoke.Environment) {
				ctx, cancel := context.WithCancel(t.Context())

				process, err := env.Start(ctx, invoke.ShellCommand("sleep 30"))
				require.NoError(t, err)
				require.NotNil(t, process)

				defer func() {
					_ = process.Close()
				}()

				cancel()

				done := make(chan error, 1)

				go func() {
					_, waitErr := process.Wait()
					done <- waitErr
				}()

				select {
				case err := <-done:
					require.Error(t, err)
				case <-time.After(lifecycleTimeout):
					_ = process.Close()

					t.FailNow()
				}
			},
		},
		{
			Category:    CategoryLifecycle,
			Name:        "signal-after-close-returns-error",
			Description: "Signaling a closed process should return an error",
			Run: func(t T, env invoke.Environment) {
				process, err := env.Start(t.Context(), invoke.ShellCommand("sleep 30"))
				require.NoError(t, err)
				require.NotNil(t, process)

				require.NoError(t, process.Close())
				require.Error(t, process.Signal(os.Kill))
			},
		},
	}
}
