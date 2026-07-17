package invoke_test

import (
	"testing"

	"github.com/ruffel/invoke"
)

func TestNew(t *testing.T) {
	t.Parallel()

	cmd := invoke.New("echo", "hello", "world")

	if cmd.Path != "echo" {
		t.Errorf("Path = %q, want %q", cmd.Path, "echo")
	}

	if len(cmd.Args) != 2 || cmd.Args[0] != "hello" || cmd.Args[1] != "world" {
		t.Errorf("Args = %q, want [hello world]", cmd.Args)
	}

	if cmd.Env != nil || cmd.Dir != "" {
		t.Errorf("Env/Dir not zero: env=%q dir=%q", cmd.Env, cmd.Dir)
	}
}

func TestShell(t *testing.T) {
	t.Parallel()

	cmd := invoke.Shell("ls -la | grep foo")

	if cmd.Path != "sh" {
		t.Errorf("Path = %q, want sh", cmd.Path)
	}

	if len(cmd.Args) != 2 || cmd.Args[0] != "-c" || cmd.Args[1] != "ls -la | grep foo" {
		t.Errorf("Args = %q, want [-c script]", cmd.Args)
	}
}

func TestCommandValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cmd     invoke.Command
		wantErr bool
	}{
		{name: "valid bare name", cmd: invoke.New("echo"), wantErr: false},
		{name: "valid absolute path", cmd: invoke.New("/bin/echo", "hi"), wantErr: false},
		{name: "zero value", cmd: invoke.Command{}, wantErr: true},
		{name: "empty path", cmd: invoke.New(""), wantErr: true},
		{name: "whitespace path", cmd: invoke.New("   "), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cmd.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
