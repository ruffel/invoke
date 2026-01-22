package ssh

import (
	"testing"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
)

func TestBuildEnvPrefix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  []string
		want string
	}{
		{
			name: "empty",
			env:  nil,
			want: "",
		},
		{
			name: "basic",
			env:  []string{"FOO=bar", "BAZ=qux"},
			want: "export FOO='bar'; export BAZ='qux'; ",
		},
		{
			name: "escaping",
			env:  []string{"MSG=don't stop"},
			want: "export MSG='don'\\''t stop'; ",
		},
		{
			name: "malformed_skipped",
			env:  []string{"INVALID"},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := buildEnvPrefix(tt.env)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBuildDirPrefix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dir  string
		want string
	}{
		{
			name: "empty",
			dir:  "",
			want: "",
		},
		{
			name: "basic",
			dir:  "/tmp/test",
			want: "cd '/tmp/test' && ",
		},
		{
			name: "escaping",
			dir:  "/tmp/O'Neil",
			want: "cd '/tmp/O'\\''Neil' && ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := buildDirPrefix(tt.dir)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBuildFullCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cmd  string
		args []string
		dir  string
		env  []string
		want string
	}{
		{
			name: "with env and dir",
			cmd:  "echo",
			args: []string{"hello"},
			dir:  "/tmp",
			env:  []string{"A=B"},
			want: "export A='B'; cd '/tmp' && 'echo' 'hello'",
		},
		{
			name: "with embedded single quote",
			cmd:  "echo",
			args: []string{"it's working"},
			dir:  "",
			env:  nil,
			want: "'echo' 'it'\\''s working'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cmd := invoke.NewCommand(tt.cmd, tt.args...)
			cmd.Dir = tt.dir
			cmd.Env = tt.env

			got := buildFullCommand(cmd)
			assert.Equal(t, tt.want, got)
		})
	}
}
