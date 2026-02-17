package invoketest

import (
	"path/filepath"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/require"
)

func environmentContracts() []TestCase {
	return []TestCase{
		{
			Category:    CategoryEnvironment,
			Name:        "close-idempotent",
			Description: "Closing an environment multiple times is deterministic and non-fatal",
			Run: func(t T, env invoke.Environment) {
				require.NoError(t, env.Close())
				require.NoError(t, env.Close())
			},
		},
		{
			Category:    CategoryEnvironment,
			Name:        "close-post-run-fails",
			Description: "Run fails deterministically after environment close",
			Run: func(t T, env invoke.Environment) {
				require.NoError(t, env.Close())

				_, err := env.Run(t.Context(), env.TargetOS().ShellCommand("echo invoke-contract"))
				require.Error(t, err)
			},
		},
		{
			Category:    CategoryEnvironment,
			Name:        "close-post-start-fails",
			Description: "Start fails deterministically after environment close",
			Run: func(t T, env invoke.Environment) {
				require.NoError(t, env.Close())

				_, err := env.Start(t.Context(), env.TargetOS().ShellCommand("echo invoke-contract"))
				require.Error(t, err)
			},
		},
		{
			Category:    CategoryEnvironment,
			Name:        "close-post-lookpath-fails",
			Description: "LookPath fails deterministically after environment close",
			Run: func(t T, env invoke.Environment) {
				require.NoError(t, env.Close())

				_, err := env.LookPath(t.Context(), "echo")
				require.Error(t, err)
			},
		},
		{
			Category:    CategoryEnvironment,
			Name:        "close-post-upload-fails",
			Description: "Upload fails deterministically after environment close",
			Run: func(t T, env invoke.Environment) {
				require.NoError(t, env.Close())

				src := filepath.Join(t.TempDir(), "close-upload-src.txt")
				remoteBase, _ := getTestPaths(t, env)

				err := env.Upload(t.Context(), src, joinRemote(env, remoteBase, "close-upload-dst.txt"))
				require.Error(t, err)
			},
		},
		{
			Category:    CategoryEnvironment,
			Name:        "close-post-download-fails",
			Description: "Download fails deterministically after environment close",
			Run: func(t T, env invoke.Environment) {
				require.NoError(t, env.Close())

				remoteBase, _ := getTestPaths(t, env)
				localPath := filepath.Join(t.TempDir(), "close-download-dst.txt")

				err := env.Download(t.Context(), joinRemote(env, remoteBase, "close-download-src.txt"), localPath)
				require.Error(t, err)
			},
		},
	}
}
