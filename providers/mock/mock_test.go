package mock

import (
	"context"
	"testing"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestMockEnvironment(t *testing.T) {
	t.Parallel()

	env := New()
	ctx := context.Background()

	expectedRes := &invoke.Result{ExitCode: 0}
	env.On("Run", ctx, mock.AnythingOfType("*invoke.Command")).Return(expectedRes, nil)

	res, err := env.Run(ctx, &invoke.Command{Cmd: "echo"})
	require.NoError(t, err)
	assert.Equal(t, expectedRes, res)

	env.On("Upload", ctx, "src", "dst", mock.Anything).Return(nil)

	err = env.Upload(ctx, "src", "dst")
	require.NoError(t, err)

	env.AssertExpectations(t)
}
