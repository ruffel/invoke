//go:build integration

package docker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testImage     = "alpine:latest"
	testContainer = "invoke-integration-test-container"
)

func TestIntegration(t *testing.T) {
	ctx := context.Background()

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("Skipping Docker integration test: failed to create client: %v", err)
	}
	defer cli.Close()

	// Ping to ensure daemon is up
	if _, err := cli.Ping(ctx); err != nil {
		t.Skipf("Skipping Docker integration test: daemon not reachable: %v", err)
	}

	setupContainer(t, ctx, cli)
	defer teardownContainer(ctx, cli)

	env, err := New(Config{ContainerID: testContainer})
	require.NoError(t, err)
	defer env.Close()

	t.Run("Run simple command", func(t *testing.T) {
		var stdout bytes.Buffer
		cmd := invoke.Command{
			Cmd:    "echo",
			Args:   []string{"hello", "docker"},
			Stdout: &stdout,
		}
		res, err := env.Run(ctx, &cmd)
		require.NoError(t, err)
		assert.Equal(t, 0, res.ExitCode)
		assert.Equal(t, "hello docker\n", stdout.String())
	})

	t.Run("Run error command", func(t *testing.T) {
		cmd := invoke.Command{
			Cmd:  "sh",
			Args: []string{"-c", "exit 42"},
		}
		res, err := env.Run(ctx, &cmd)
		require.Error(t, err) // ExitError
		if res != nil {
			assert.Equal(t, 42, res.ExitCode)
		}
	})

	t.Run("Environment Variables", func(t *testing.T) {
		var stdout bytes.Buffer
		cmd := invoke.Command{
			Cmd:    "sh",
			Args:   []string{"-c", "echo $MY_VAR"},
			Env:    []string{"MY_VAR=integration"},
			Stdout: &stdout,
		}
		_, err := env.Run(ctx, &cmd)
		require.NoError(t, err)
		assert.Equal(t, "integration\n", stdout.String())
	})

	t.Run("File Transfer", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create local file
		srcContent := "docker-transfer-test"
		srcFile := filepath.Join(tmpDir, "upload.txt")
		err := os.WriteFile(srcFile, []byte(srcContent), 0644)
		require.NoError(t, err)

		remotePath := "/tmp/upload.txt"

		// Upload
		err = env.Upload(ctx, srcFile, remotePath, invoke.WithPermissions(0644))
		require.NoError(t, err, "Upload failed")

		// Verify on remote
		var stdout bytes.Buffer
		catCmd := invoke.Command{Cmd: "cat", Args: []string{remotePath}, Stdout: &stdout}
		_, err = env.Run(ctx, &catCmd)
		require.NoError(t, err)
		assert.Equal(t, srcContent, stdout.String())

		// Download
		dstFile := filepath.Join(tmpDir, "download.txt")
		err = env.Download(ctx, remotePath, dstFile)
		require.NoError(t, err, "Download failed")

		// Verify local
		downloaded, err := os.ReadFile(dstFile)
		require.NoError(t, err)
		assert.Equal(t, srcContent, string(downloaded))
	})
	t.Run("Working Directory", func(t *testing.T) {
		var stdout bytes.Buffer
		// Docker default is / typically, unless WORKDIR set.
		// Let's run in /tmp
		cmd := invoke.Command{
			Cmd:    "pwd",
			Dir:    "/tmp",
			Stdout: &stdout,
		}
		_, err := env.Run(ctx, &cmd)
		require.NoError(t, err)
		assert.Equal(t, "/tmp\n", stdout.String())
	})

	t.Run("Signal", func(t *testing.T) {
		cmd := invoke.Command{Cmd: "sleep", Args: []string{"10"}}
		process, err := env.Start(ctx, &cmd)
		require.NoError(t, err)
		defer process.Close()

		time.Sleep(500 * time.Millisecond)

		err = process.Signal(os.Kill)
		require.NoError(t, err)

		err = process.Wait()
		// Docker exec signal handling can be tricky, but we expect it to return
		// often with code 137 (128+9) or similar if killed.
		if err != nil {
			var exitErr *invoke.ExitError
			if errors.As(err, &exitErr) {
				assert.NotEqual(t, 0, exitErr.ExitCode)
			}
		}
	})
}

func setupContainer(t *testing.T, ctx context.Context, cli *client.Client) {
	// Ensure cleanup of previous runs
	_ = cli.ContainerRemove(ctx, testContainer, container.RemoveOptions{Force: true})

	// Pull Image
	reader, err := cli.ImagePull(ctx, testImage, image.PullOptions{})
	if err != nil {
		t.Fatalf("Failed to pull %s: %v", testImage, err)
	}
	io.Copy(io.Discard, reader)
	reader.Close()

	// Create & Start
	_, err = cli.ContainerCreate(ctx, &container.Config{
		Image: testImage,
		Cmd:   []string{"sleep", "infinity"},
	}, nil, nil, nil, testContainer)
	if err != nil {
		t.Fatalf("Failed to create container: %v", err)
	}

	if err := cli.ContainerStart(ctx, testContainer, container.StartOptions{}); err != nil {
		t.Fatalf("Failed to start container: %v", err)
	}
}

func teardownContainer(ctx context.Context, cli *client.Client) {
	_ = cli.ContainerRemove(ctx, testContainer, container.RemoveOptions{Force: true})
}
