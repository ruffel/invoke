package invoke_test

import (
	"testing"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
)

func TestNew(t *testing.T) {
	t.Parallel()

	cmd := invoke.New("echo", "hello", "world")

	assert.Equal(t, "echo", cmd.Path)
	assert.Equal(t, []string{"hello", "world"}, cmd.Args)
	assert.Nil(t, cmd.Env)
	assert.Empty(t, cmd.Dir)
}

func TestShell(t *testing.T) {
	t.Parallel()

	cmd := invoke.Shell("ls -la | grep foo")

	assert.Equal(t, "sh", cmd.Path)
	assert.Equal(t, []string{"-c", "ls -la | grep foo"}, cmd.Args)
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
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
