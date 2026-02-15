package local

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileTransfer(t *testing.T) {
	t.Parallel()

	env, err := New()
	require.NoError(t, err)

	t.Cleanup(func() { _ = env.Close() })

	ctx := context.Background()

	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "source.txt")
	dstFile := filepath.Join(tmpDir, "dest", "target.txt")
	content := []byte("hello file transfer")

	// Create source
	err = os.WriteFile(srcFile, content, 0o644)
	require.NoError(t, err)

	t.Run("Upload (Copy)", func(t *testing.T) {
		t.Parallel()

		err := env.Upload(ctx, srcFile, dstFile, invoke.WithPermissions(0o600))
		require.NoError(t, err)

		// Verify content
		readContent, err := os.ReadFile(dstFile)
		require.NoError(t, err)
		assert.Equal(t, content, readContent)

		// Verify perms (skip on Windows as perms are loose)
		if runtime.GOOS != "windows" {
			info, err := os.Stat(dstFile)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		}
	})

	t.Run("Download (Copy)", func(t *testing.T) {
		t.Parallel()

		// Prepare a separate file specifically for download test to avoid race with Upload test
		downloadSrc := filepath.Join(tmpDir, "download_source.txt")
		require.NoError(t, os.WriteFile(downloadSrc, content, 0o644))

		downloadDst := filepath.Join(tmpDir, "downloaded.txt")
		err := env.Download(ctx, downloadSrc, downloadDst)
		require.NoError(t, err)

		readContent, err := os.ReadFile(downloadDst)
		require.NoError(t, err)
		assert.Equal(t, content, readContent)
	})

	t.Run("Recursive Directory", func(t *testing.T) {
		t.Parallel()

		srcDir := filepath.Join(tmpDir, "src_tree")
		dstDir := filepath.Join(tmpDir, "dst_tree")

		require.NoError(t, os.MkdirAll(filepath.Join(srcDir, "sub"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(srcDir, "sub", "file.txt"), content, 0o644))

		err := env.Upload(ctx, srcDir, dstDir)
		require.NoError(t, err)

		// Verify
		readContent, err := os.ReadFile(filepath.Join(dstDir, "sub", "file.txt"))
		require.NoError(t, err)
		assert.Equal(t, content, readContent)
	})
}

func TestFileTransfer_Validation(t *testing.T) {
	t.Parallel()

	env, err := New()
	require.NoError(t, err)

	t.Cleanup(func() { _ = env.Close() })

	ctx := context.Background()
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "source.txt")
	dstFile := filepath.Join(tmpDir, "dest.txt")

	require.NoError(t, os.WriteFile(srcFile, []byte("content"), 0o644))

	t.Run("owner option unsupported", func(t *testing.T) {
		t.Parallel()

		err := env.Upload(ctx, srcFile, dstFile, invoke.WithOwner(1000, 1000))
		require.Error(t, err)
		assert.ErrorIs(t, err, invoke.ErrNotSupported)
	})

	t.Run("recursive disabled for directories", func(t *testing.T) {
		t.Parallel()

		srcDir := filepath.Join(tmpDir, "src_tree")
		dstDir := filepath.Join(tmpDir, "dst_tree")
		require.NoError(t, os.MkdirAll(filepath.Join(srcDir, "sub"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(srcDir, "sub", "file.txt"), []byte("x"), 0o644))

		err := env.Upload(ctx, srcDir, dstDir, func(cfg *invoke.FileConfig) { cfg.Recursive = false })
		require.Error(t, err)
		assert.Contains(t, err.Error(), "recursive directory upload is disabled")
	})

	t.Run("upload fails when environment is closed", func(t *testing.T) {
		t.Parallel()

		localEnv, err := New()
		require.NoError(t, err)

		_ = localEnv.Close()

		err = localEnv.Upload(ctx, srcFile, dstFile)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "environment is closed")
	})
}
