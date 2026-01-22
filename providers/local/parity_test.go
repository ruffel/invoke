package local_test

import (
	"testing"

	"github.com/ruffel/invoke/invoketest"
	"github.com/ruffel/invoke/providers/local"
	"github.com/stretchr/testify/require"
)

func TestLocalParity(t *testing.T) {
	t.Parallel()

	env, err := local.New()
	require.NoError(t, err)

	defer func() {
		_ = env.Close()
	}()

	invoketest.Verify(t, env)
}
