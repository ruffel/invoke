package docker

import (
	"testing"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTargetOS(t *testing.T) {
	t.Parallel()
	t.Run("default", func(t *testing.T) {
		t.Parallel()

		cfg := Config{ContainerID: "foo"}
		env, err := New(WithConfig(cfg))
		require.NoError(t, err)
		assert.Equal(t, invoke.OSLinux, env.TargetOS())
	})

	// Test Override (Windows)
	t.Run("windows", func(t *testing.T) {
		t.Parallel()

		cfg := Config{ContainerID: "foo", OS: invoke.OSWindows}
		env, err := New(WithConfig(cfg))
		require.NoError(t, err)
		assert.Equal(t, invoke.OSWindows, env.TargetOS())
	})
}

func TestConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  Config
		wantErr bool
	}{
		{
			name:    "valid",
			config:  Config{ContainerID: "123"},
			wantErr: false,
		},
		{
			name:    "missing container id",
			config:  Config{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.config.Validate()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
