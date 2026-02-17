package invoketest

import (
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func getTestPaths(t T, env invoke.Environment) (string, string) {
	name := strings.ReplaceAll(t.Name(), "/", "_")

	if env.TargetOS() == invoke.OSWindows {
		return `C:\Windows\Temp\invoke-test-` + name, `Get-Content -Raw`
	}

	return "/tmp/invoke-test-" + name, "cat"
}

// joinRemote handles path joining for the target environment.
func joinRemote(env invoke.Environment, base string, parts ...string) string {
	if env.TargetOS() == invoke.OSWindows {
		var res strings.Builder
		res.WriteString(base)

		for _, p := range parts {
			res.WriteString("\\" + p)
		}

		return res.String()
	}

	// Use forward slash for Unix
	return path.Join(append([]string{base}, parts...)...)
}

//nolint:funlen // Test functions are often longer due to setup/assertions.
func fileContracts() []TestCase {
	return []TestCase{
		{
			Category:    CategoryFilesystem,
			Name:        "upload-failure-source-missing",
			Description: "Error returned when we try to upload a non-existent local file",
			Run: func(t T, env invoke.Environment) {
				dstBase, _ := getTestPaths(t, env)
				src := filepath.Join(t.TempDir(), "this-file-really-does-not-exist-12345")
				dst := joinRemote(env, dstBase, "should-not-exist-12345")

				err := env.Upload(t.Context(), src, dst)
				require.Error(t, err)
			},
		},
		{
			Category:    CategoryFilesystem,
			Name:        "upload-success-with-dir-create",
			Description: "Successfully upload a single file to non-existent directory",
			Run: func(t T, env invoke.Environment) {
				// ARRANGE
				content := "hello world from invoke"
				dstBase, readCmd := getTestPaths(t, env)

				dstPath := joinRemote(env, dstBase, "test.txt")
				srcPath := filepath.Join(t.TempDir(), "test.txt")
				require.NoError(t, os.WriteFile(srcPath, []byte(content), 0o644))

				// ACT
				err := env.Upload(t.Context(), srcPath, dstPath)
				require.NoError(t, err)

				// ASSERT
				exec := invoke.NewExecutor(env)
				assert.NotNil(t, exec)

				res, err := exec.RunBuffered(t.Context(), env.TargetOS().ShellCommand(readCmd+" "+dstPath))
				require.NoError(t, err)
				require.Equal(t, content, strings.TrimSpace(string(res.Stdout)))
				require.Zero(t, res.ExitCode)
			},
		},
		{
			Category:    CategoryFilesystem,
			Name:        "upload-success-with-nested-dir",
			Description: "Successfully upload a single file to a nested directory structure that does not exist",
			Run: func(t T, env invoke.Environment) {
				// ARRANGE
				content := "hello world from invoke"
				dstBase, readCmd := getTestPaths(t, env)

				dstPath := joinRemote(env, dstBase, "nested", "dir", "structure", "level1", "level2", "test.txt")
				srcPath := filepath.Join(t.TempDir(), "test.txt")
				require.NoError(t, os.WriteFile(srcPath, []byte(content), 0o644))

				// ACT
				err := env.Upload(t.Context(), srcPath, dstPath)
				require.NoError(t, err)

				// ASSERT
				exec := invoke.NewExecutor(env)
				assert.NotNil(t, exec)

				res, err := exec.RunBuffered(t.Context(), env.TargetOS().ShellCommand(readCmd+" "+dstPath))
				require.NoError(t, err)
				require.Equal(t, content, strings.TrimSpace(string(res.Stdout)))
				require.Zero(t, res.ExitCode)
			},
		},
		{
			Category:    CategoryFilesystem,
			Name:        "upload-success-overwrite",
			Description: "Successfully upload a single file and overwrite existing content",
			Run: func(t T, env invoke.Environment) {
				// ARRANGE
				initialContent := "initial content"
				updatedContent := "updated content"
				dstBase, readCmd := getTestPaths(t, env)

				dstPath := joinRemote(env, dstBase, "test.txt")
				srcPath := filepath.Join(t.TempDir(), "test.txt")

				// Create the initial file on the source.
				require.NoError(t, os.WriteFile(srcPath, []byte(initialContent), 0o644))

				// ACT

				// Upload the initial file to the destination.
				err := env.Upload(t.Context(), srcPath, dstPath)
				require.NoError(t, err)

				// Assert the initial content is correct on the target.
				exec := invoke.NewExecutor(env)
				assert.NotNil(t, exec)

				res, err := exec.RunBuffered(t.Context(), env.TargetOS().ShellCommand(readCmd+" "+dstPath))
				require.NoError(t, err)
				require.Equal(t, initialContent, strings.TrimSpace(string(res.Stdout)))
				require.Zero(t, res.ExitCode)

				// Update the source file with new content.
				require.NoError(t, os.WriteFile(srcPath, []byte(updatedContent), 0o644))

				// Upload the updated file to the destination (overwriting).
				err = env.Upload(t.Context(), srcPath, dstPath)
				require.NoError(t, err)

				// Assert the updated content is correct on the target.
				res, err = exec.RunBuffered(t.Context(), env.TargetOS().ShellCommand(readCmd+" "+dstPath))
				require.NoError(t, err)
				require.Equal(t, updatedContent, strings.TrimSpace(string(res.Stdout)))
				require.Zero(t, res.ExitCode)
			},
		},
		{
			Category:    CategoryFilesystem,
			Name:        "upload-recursive-directory",
			Description: "Upload a directory tree containing subdirectories and files",
			Run: func(t T, env invoke.Environment) {
				// ARRANGE: Create local structure:
				// /dir
				//   /file1.txt
				//   /subdir
				//     /file2.txt
				tempDir := t.TempDir()
				srcDir := filepath.Join(tempDir, "upload-tree")
				require.NoError(t, os.MkdirAll(filepath.Join(srcDir, "subdir"), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(srcDir, "file1.txt"), []byte("root file"), 0o644))
				require.NoError(t, os.WriteFile(filepath.Join(srcDir, "subdir", "file2.txt"), []byte("sub file"), 0o644))

				dstBase, readCmd := getTestPaths(t, env)
				dstDir := joinRemote(env, dstBase, "tree")

				// ACT
				err := env.Upload(t.Context(), srcDir, dstDir)
				require.NoError(t, err)

				// ASSERT
				exec := invoke.NewExecutor(env)
				// Check file 1
				res, err := exec.RunBuffered(t.Context(), env.TargetOS().ShellCommand(readCmd+" "+joinRemote(env, dstDir, "file1.txt")))
				require.NoError(t, err)
				require.Equal(t, "root file", strings.TrimSpace(string(res.Stdout)))

				// Check file 2 (nested)
				res, err = exec.RunBuffered(t.Context(), env.TargetOS().ShellCommand(readCmd+" "+joinRemote(env, dstDir, "subdir", "file2.txt")))
				require.NoError(t, err)
				require.Equal(t, "sub file", strings.TrimSpace(string(res.Stdout)))
			},
		},
		{
			Category:    CategoryFilesystem,
			Name:        "download-file-content",
			Description: "Download copies file content from remote path to local path",
			Run: func(t T, env invoke.Environment) {
				content := "download contract content"

				seedLocalPath := filepath.Join(t.TempDir(), "seed-source.txt")
				require.NoError(t, os.WriteFile(seedLocalPath, []byte(content), 0o644))

				remoteBase, _ := getTestPaths(t, env)
				remoteSourcePath := joinRemote(env, remoteBase, "download-source.txt")
				require.NoError(t, env.Upload(t.Context(), seedLocalPath, remoteSourcePath))

				downloadedPath := filepath.Join(t.TempDir(), "downloaded.txt")
				require.NoError(t, env.Download(t.Context(), remoteSourcePath, downloadedPath))

				downloadedContent, err := os.ReadFile(downloadedPath)
				require.NoError(t, err)
				require.Equal(t, content, strings.TrimSpace(string(downloadedContent)))
			},
		},
		{
			Category:    CategoryFilesystem,
			Name:        "download-creates-local-parents",
			Description: "Download creates missing local destination parent directories",
			Run: func(t T, env invoke.Environment) {
				content := "download parent create content"

				seedLocalPath := filepath.Join(t.TempDir(), "parent-seed-source.txt")
				require.NoError(t, os.WriteFile(seedLocalPath, []byte(content), 0o644))

				remoteBase, _ := getTestPaths(t, env)
				remoteSourcePath := joinRemote(env, remoteBase, "download-parent-source.txt")
				require.NoError(t, env.Upload(t.Context(), seedLocalPath, remoteSourcePath))

				localBase := filepath.Join(t.TempDir(), "nested", "local", "download")
				downloadedPath := filepath.Join(localBase, "file.txt")

				_, err := os.Stat(localBase)
				require.Error(t, err)
				require.True(t, os.IsNotExist(err))

				require.NoError(t, env.Download(t.Context(), remoteSourcePath, downloadedPath))

				stat, err := os.Stat(localBase)
				require.NoError(t, err)
				require.True(t, stat.IsDir())

				downloadedContent, err := os.ReadFile(downloadedPath)
				require.NoError(t, err)
				require.Equal(t, content, strings.TrimSpace(string(downloadedContent)))
			},
		},
	}
}
