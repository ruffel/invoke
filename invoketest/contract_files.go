package invoketest

import (
	"os"
	"path/filepath"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fileContracts() []TestCase {
	return []TestCase{
		{
			Category:    CategoryFilesystem,
			Name:        "upload-failure-source-missing",
			Description: "Error returned when we try to upload a non-existent local file",
			Run: func(t T, env invoke.Environment) {
				// Create a directory that we know exists, but look for a file inside it that does NOT exist.
				src := filepath.Join(t.TempDir(), "this-file-really-does-not-exist-12345")
				dst := "/tmp/should-not-exist-on-target-12345"

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

				// The destination path does not exist, the
				dstPath := "/tmp/invoke-test-" + t.Name() + "/test.txt"
				srcPath := filepath.Join(t.TempDir(), "test.txt")
				require.NoError(t, os.WriteFile(srcPath, []byte(content), 0644))

				// ACT
				err := env.Upload(t.Context(), srcPath, dstPath)
				require.NoError(t, err)

				// ASSERT
				exec := invoke.NewExecutor(env)
				assert.NotNil(t, exec)

				res, err := exec.RunBuffered(t.Context(), invoke.NewCommand("cat", dstPath))
				require.NoError(t, err)
				require.Equal(t, content, string(res.Stdout))
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

				dstPath := "/tmp/invoke-test-" + t.Name() + "/nested/dir/structure/level1/level2/test.txt"
				srcPath := filepath.Join(t.TempDir(), "test.txt")
				require.NoError(t, os.WriteFile(srcPath, []byte(content), 0644))

				// ACT
				err := env.Upload(t.Context(), srcPath, dstPath)
				require.NoError(t, err)

				// ASSERT
				exec := invoke.NewExecutor(env)
				assert.NotNil(t, exec)

				res, err := exec.RunBuffered(t.Context(), invoke.NewCommand("cat", dstPath))
				require.NoError(t, err)
				require.Equal(t, content, string(res.Stdout))
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

				dstPath := "/tmp/invoke-test-" + t.Name() + "/test.txt"
				srcPath := filepath.Join(t.TempDir(), "test.txt")

				// Create the initial file on the source.
				require.NoError(t, os.WriteFile(srcPath, []byte(initialContent), 0644))

				// ACT

				// Upload the initial file to the destination.
				err := env.Upload(t.Context(), srcPath, dstPath)
				require.NoError(t, err)

				// Assert the initial content is correct on the target.
				exec := invoke.NewExecutor(env)
				assert.NotNil(t, exec)

				res, err := exec.RunBuffered(t.Context(), invoke.NewCommand("cat", dstPath))
				require.NoError(t, err)
				require.Equal(t, initialContent, string(res.Stdout))
				require.Zero(t, res.ExitCode)

				// Update the source file with new content.
				require.NoError(t, os.WriteFile(srcPath, []byte(updatedContent), 0644))

				// Upload the updated file to the destination (overwriting).
				err = env.Upload(t.Context(), srcPath, dstPath)
				require.NoError(t, err)

				// Assert the updated content is correct on the target.
				res, err = exec.RunBuffered(t.Context(), invoke.NewCommand("cat", dstPath))
				require.NoError(t, err)
				require.Equal(t, updatedContent, string(res.Stdout))
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
				require.NoError(t, os.MkdirAll(filepath.Join(srcDir, "subdir"), 0755))
				require.NoError(t, os.WriteFile(filepath.Join(srcDir, "file1.txt"), []byte("root file"), 0644))
				require.NoError(t, os.WriteFile(filepath.Join(srcDir, "subdir", "file2.txt"), []byte("sub file"), 0644))

				dstDir := "/tmp/invoke-tree-" + t.Name()

				// ACT
				err := env.Upload(t.Context(), srcDir, dstDir)
				require.NoError(t, err)

				// ASSERT
				exec := invoke.NewExecutor(env)
				// Check file 1
				res, err := exec.RunBuffered(t.Context(), invoke.NewCommand("cat", filepath.Join(dstDir, "file1.txt")))
				require.NoError(t, err)
				require.Equal(t, "root file", string(res.Stdout))

				// Check file 2 (nested)
				res, err = exec.RunBuffered(t.Context(), invoke.NewCommand("cat", filepath.Join(dstDir, "subdir", "file2.txt")))
				require.NoError(t, err)
				require.Equal(t, "sub file", string(res.Stdout))
			},
		},
	}
}
