package ssh

import (
	"testing"
	"time"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
)

func TestTargetOS(t *testing.T) {
	t.Parallel()
	// Default (Linux)
	t.Run("default", func(t *testing.T) {
		t.Parallel()

		c := NewConfig("example.com", "root").WithDefaults()
		assert.Equal(t, invoke.OSLinux, c.OS)
	})

	// Override (Windows)
	t.Run("windows", func(t *testing.T) {
		t.Parallel()

		c := NewConfig("example.com", "root")
		c.OS = invoke.OSWindows
		c = c.WithDefaults()
		assert.Equal(t, invoke.OSWindows, c.OS)
	})
}

func TestConfig_WithDefaults(t *testing.T) {
	t.Parallel()
	// Test constructor defaults
	c := NewConfig("example.com", "root")
	c.InsecureSkipVerify = true // Enable this to get a default HostKeyCheck
	c = c.WithDefaults()

	assert.Equal(t, 22, c.Port)
	assert.Equal(t, 10*time.Second, c.Timeout)
	assert.NotNil(t, c.HostKeyCheck)
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
			config:  Config{Host: "example.com", User: "root", InsecureSkipVerify: true}.WithDefaults(),
			wantErr: false,
		},
		{
			name:    "missing host",
			config:  Config{User: "root"},
			wantErr: true,
		},
		{
			name:    "missing user",
			config:  Config{Host: "example.com"},
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
