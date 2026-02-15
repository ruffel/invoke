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

				binary := "echo"
				if env.TargetOS() == invoke.OSWindows {
					binary = "cmd.exe"
				}

				path, err := exec.LookPath(t.Context(), binary)

				require.NoError(t, err)
				assert.NotEmpty(t, path)
			},
		},
	}
}
