//go:build docker

package docker_test

import (
	"context"
	"testing"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/docker"
	"github.com/ruffel/invoke/invoketest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dialContainer returns an Environment for a freshly started container.
func dialContainer(t *testing.T) *docker.Environment {
	t.Helper()

	id := startContainer(t)

	env, err := docker.New(t.Context(), id, docker.WithHost(daemonHost(t)))
	require.NoError(t, err, "docker.New")

	t.Cleanup(func() { _ = env.Close() })

	return env
}

// asTestingT recovers the *testing.T the contract runs under, whose
// Cleanup fires when the contract ends — the right scope for tearing down
// a per-contract container.
func asTestingT(it invoketest.T) *testing.T {
	tt, ok := it.(*testing.T)
	require.True(tt, ok, "docker contract tests require the standard *testing.T")

	return tt
}

// TestDockerContractSuite runs the shared behavioral contracts against a
// real container.
func TestDockerContractSuite(t *testing.T) {
	t.Parallel()

	invoketest.Verify(t, func(it invoketest.T) invoke.Environment {
		return dialContainer(asTestingT(it))
	})
}

func TestMissingContainerIsNotFound(t *testing.T) {
	t.Parallel()

	_, err := docker.New(t.Context(), "invoke-no-such-container-9f3a1c", docker.WithHost(daemonHost(t)))
	assert.ErrorIs(t, err, invoke.ErrNotFound, "a missing container must be reported as not found")
}

func TestClosedEnvironmentRefusesEverything(t *testing.T) {
	t.Parallel()

	env := dialContainer(t)
	require.NoError(t, env.Close())

	ctx := context.Background()

	_, err := env.Start(ctx, invoke.New("true"), invoke.IO{})
	assert.ErrorIs(t, err, invoke.ErrClosed, "Start after Close")

	_, err = env.LookPath(ctx, "sh")
	assert.ErrorIs(t, err, invoke.ErrClosed, "LookPath after Close")
}
