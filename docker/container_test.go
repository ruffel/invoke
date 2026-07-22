//go:build docker

// Package docker's contract tests need a running daemon, so they are
// behind the "docker" build tag and run in the integration lane.
package docker_test

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/stretchr/testify/require"
)

// testImage is a small image with a shell and the coreutils the contracts
// exercise.
const testImage = "alpine:3"

// daemonHost returns the endpoint the local docker installation is
// actually using. DOCKER_HOST wins when set; otherwise the current
// context is consulted, since installations like Colima, Rancher and
// Docker Desktop keep their socket outside the default location and
// record it there rather than in the environment.
func daemonHost(t *testing.T) string {
	t.Helper()

	if host := os.Getenv("DOCKER_HOST"); host != "" {
		return host
	}

	out, err := exec.CommandContext(t.Context(), "docker", "context", "inspect",
		"--format", "{{.Endpoints.docker.Host}}").Output()
	if err != nil {
		return "" // Fall back to the SDK's own defaults.
	}

	return strings.TrimSpace(string(out))
}

// newClient connects to whichever daemon the local installation uses.
func newClient(t *testing.T) *client.Client {
	t.Helper()

	opts := []client.Opt{client.FromEnv, client.WithAPIVersionNegotiation()}
	if host := daemonHost(t); host != "" {
		opts = append(opts, client.WithHost(host))
	}

	cli, err := client.NewClientWithOpts(opts...)
	require.NoError(t, err, "connecting to the daemon")

	return cli
}

// startContainer launches a container that idles until the test finishes,
// and returns its ID.
func startContainer(t *testing.T) string {
	t.Helper()

	return startContainerWith(t, &container.HostConfig{})
}

// startContainerWith starts the test image under a caller-supplied host
// configuration, for the few tests that need one — a read-only mount, say.
func startContainerWith(t *testing.T, hostCfg *container.HostConfig) string {
	t.Helper()

	cli := newClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pullImage(ctx, t, cli)

	created, err := cli.ContainerCreate(ctx, &container.Config{
		Image: testImage,
		// Idle forever: the contracts exec into a running container.
		Cmd: []string{"sleep", "infinity"},
	}, hostCfg, nil, nil, "")
	require.NoError(t, err, "creating the container")

	require.NoError(t, cli.ContainerStart(ctx, created.ID, container.StartOptions{}), "starting the container")

	t.Cleanup(func() {
		removeCtx, removeCancel := context.WithTimeout(context.Background(), time.Minute)
		defer removeCancel()

		_ = cli.ContainerRemove(removeCtx, created.ID, container.RemoveOptions{Force: true})
		_ = cli.Close()
	})

	return created.ID
}

// pullImage fetches the test image unless the daemon already has it.
func pullImage(ctx context.Context, t *testing.T, cli *client.Client) {
	t.Helper()

	if _, err := cli.ImageInspect(ctx, testImage); err == nil {
		return
	}

	body, err := cli.ImagePull(ctx, testImage, image.PullOptions{})
	require.NoError(t, err, "pulling %s", testImage)

	defer func() { _ = body.Close() }()

	// The pull only completes once its progress stream is drained.
	_, err = io.Copy(io.Discard, body)
	require.NoError(t, err, "draining the image pull")
}
