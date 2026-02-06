package ssh

import (
	"testing"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
)

func TestBuildEnvPrefix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		env       []string
		isWindows bool
		want      string
	}{
		{
			name:      "empty",
			env:       nil,
			isWindows: false,
			want:      "",
		},
		{
			name:      "posix_basic",
			env:       []string{"FOO=bar", "BAZ=qux"},
			isWindows: false,
			want:      "export FOO='bar'; export BAZ='qux'; ",
		},
		{
			name:      "posix_escaping",
			env:       []string{"MSG=don't stop"},
			isWindows: false,
			want:      "export MSG='don'\\''t stop'; ",
		},
		{
			name:      "windows_basic",
			env:       []string{"FOO=bar"},
			isWindows: true,
			want:      "$env:FOO='bar'; ",
		},
		{
			name:      "windows_escaping",
			env:       []string{"MSG=don't stop"},
			isWindows: true,
			want:      "$env:MSG='don'\\''t stop'; ",
		},
		{
			name:      "malformed_skipped",
			env:       []string{"INVALID"},
			isWindows: false,
			want:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := buildEnvPrefix(tt.env, tt.isWindows)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBuildDirPrefix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		dir       string
		isWindows bool
		want      string
	}{
		{
			name:      "empty",
			dir:       "",
			isWindows: false,
			want:      "",
		},
		{
			name:      "posix_basic",
			dir:       "/tmp/test",
			isWindows: false,
			want:      "cd '/tmp/test' && ",
		},
		{
			name:      "posix_escaping",
			dir:       "/tmp/O'Neil",
			isWindows: false,
			want:      "cd '/tmp/O'\\''Neil' && ",
		},
		{
			name:      "windows_basic",
			dir:       "C:\\Windows",
			isWindows: true,
			want:      "cd 'C:\\Windows'; ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := buildDirPrefix(tt.dir, tt.isWindows)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBuildFullCommand(t *testing.T) {
	t.Parallel()

	cmd := invoke.NewCommand("echo", "hello")
	cmd.Dir = "/tmp"
	cmd.Env = []string{"A=B"}

	got := buildFullCommand(cmd, false)
	want := "export A='B'; cd '/tmp' && echo 'hello'"
	assert.Equal(t, want, got)
}
