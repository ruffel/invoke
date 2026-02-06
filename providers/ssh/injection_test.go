package ssh

import (
	"testing"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
)

func TestSSH_Security_CommandInjection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     []string
		expected string
	}{
		{
			name:     "semicolon with space",
			args:     []string{"hello; whoami"},
			expected: "echo 'hello; whoami'",
		},
		{
			name:     "semicolon no space",
			args:     []string{"hello;whoami"},
			expected: "echo 'hello;whoami'",
		},
		{
			name:     "embedded single quote",
			args:     []string{"it's"},
			expected: "echo 'it'\\''s'",
		},
		{
			name:     "pipe",
			args:     []string{"foo|bar"},
			expected: "echo 'foo|bar'",
		},
		{
			name:     "backticks",
			args:     []string{"`whoami`"},
			expected: "echo '`whoami`'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd := invoke.NewCommand("echo", tt.args...)
			got := buildFullCommand(cmd, false)
			assert.Equal(t, tt.expected, got)
		})
	}
}
