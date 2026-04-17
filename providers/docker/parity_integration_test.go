//go:build integration

package docker

import (
	"context"
	"testing"

	"github.com/docker/docker/client"
	"github.com/ruffel/invoke/invoketest"
	"github.com/stretchr/testify/require"
)

func TestParity(t *testing.T) {
	ctx := context.Background()

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("Skipping Docker integration parity test: failed to create client: %v", err)
	}
	defer cli.Close()

	if _, err := cli.Ping(ctx); err != nil {
		t.Skipf("Skipping Docker integration parity test: daemon not reachable: %v", err)
	}

	setupContainer(t, ctx, cli)
	defer teardownContainer(ctx, cli)

	env, err := New(WithConfig(Config{ContainerID: testContainer}))
	require.NoError(t, err)

	defer func() {
		_ = env.Close()
	}()

	invoketest.Verify(t, env)
}
