package invoketest

import (
	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func systemContracts() []TestCase {
	return []TestCase{
		{
			Category: CategorySystem,
			Name:     "lookpath",
			Run: func(t T, env invoke.Environment) {
				exec := invoke.NewExecutor(env)

				path, err := exec.LookPath(t.Context(), "echo")

				require.NoError(t, err)
				assert.NotEmpty(t, path)
			},
		},
		{
			Category:    CategorySystem,
			Name:        "lookpath-not-found",
			Description: "LookPath returns an error for a binary that does not exist",
			Run: func(t T, env invoke.Environment) {
				exec := invoke.NewExecutor(env)

				_, err := exec.LookPath(t.Context(), "invoke-definitely-not-a-real-binary-xyz")

				require.Error(t, err)
			},
		},
	}
}
