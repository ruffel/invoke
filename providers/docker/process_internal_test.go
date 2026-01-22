package docker

import (
	"strings"
	"testing"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
)

func TestBuildExecConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cmd  *invoke.Command
		want struct {
			Cmd         []string
			Env         []string
			WorkingDir  string
			AttachStdin bool
			Tty         bool
		}
	}{
		{
			name: "basic command",
			cmd:  invoke.NewCommand("echo", "hello", "world"),
			want: struct {
				Cmd         []string
				Env         []string
				WorkingDir  string
				AttachStdin bool
				Tty         bool
			}{
				Cmd: []string{"echo", "hello", "world"},
			},
		},
		{
			name: "with environment and dir",
			cmd: &invoke.Command{
				Cmd:  "sh",
				Args: []string{"-c", "ls"},
				Env:  []string{"FOO=bar"},
				Dir:  "/tmp",
			},
			want: struct {
				Cmd         []string
				Env         []string
				WorkingDir  string
				AttachStdin bool
				Tty         bool
			}{
				Cmd:        []string{"sh", "-c", "ls"},
				Env:        []string{"FOO=bar"},
				WorkingDir: "/tmp",
			},
		},
		{
			name: "interactive tty",
			cmd: &invoke.Command{
				Cmd:   "bash",
				Stdin: strings.NewReader(""),
				Tty:   true,
			},
			want: struct {
				Cmd         []string
				Env         []string
				WorkingDir  string
				AttachStdin bool
				Tty         bool
			}{
				Cmd:         []string{"bash"},
				AttachStdin: true,
				Tty:         true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := buildExecConfig(tt.cmd)

			assert.Equal(t, tt.want.Cmd, got.Cmd, "Cmd mismatch")
			assert.Equal(t, tt.want.Env, got.Env, "Env mismatch")
			assert.Equal(t, tt.want.WorkingDir, got.WorkingDir, "WorkingDir mismatch")
			assert.Equal(t, tt.want.AttachStdin, got.AttachStdin, "AttachStdin mismatch")
			assert.Equal(t, tt.want.Tty, got.Tty, "Tty mismatch")

			// Always expect stdout/stderr to be attached
			assert.True(t, got.AttachStdout, "AttachStdout must always be true")
			assert.True(t, got.AttachStderr, "AttachStderr must always be true")
		})
	}
}

func TestBuildAttachConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cmd  *invoke.Command
		want bool // Tty
	}{
		{
			name: "no tty",
			cmd:  invoke.NewCommand("ls"),
			want: false,
		},
		{
			name: "with tty",
			cmd:  &invoke.Command{Tty: true},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := buildAttachConfig(tt.cmd)
			assert.Equal(t, tt.want, got.Tty, "Tty mismatch")
		})
	}
}
