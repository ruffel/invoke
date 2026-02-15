package invoketest

import (
	"strings"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func coreContracts() []TestCase {
	return []TestCase{
		{
			Category: CategoryCore,
			Name:     "simple-echo",
			Run: func(t T, env invoke.Environment) {
				exec := invoke.NewExecutor(env)
				result, err := exec.RunBuffered(t.Context(), invoke.NewCommand("echo", "hello"))
				require.NoError(t, err)
				require.NotNil(t, result)

				assert.Equal(t, "hello", strings.TrimSpace(string(result.Stdout)))
				assert.Equal(t, 0, result.ExitCode)
			},
		},
	}
}
