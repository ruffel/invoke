//go:build integration

package ssh

import (
	"testing"

	"github.com/ruffel/invoke/invoketest"
	"github.com/stretchr/testify/require"
)

func TestParity(t *testing.T) {
	config, cleanup := setupSSHEnvironment(t)
	defer cleanup()

	env, err := New(WithConfig(config))
	require.NoError(t, err)

	defer func() {
		_ = env.Close()
	}()

	invoketest.Verify(t, env)
}
