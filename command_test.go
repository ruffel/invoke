package invoke_test

import (
	"testing"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// TestValidateRefusesUnusableEnvironmentEntries pins the environment names
// a Command may carry.
//
// The names matter beyond tidiness: one provider has to render the
// environment as shell text, where a name carrying punctuation stops being
// a name and becomes commands to run. Refusing the same names on every
// target keeps that from depending on where the command happens to run.
func TestValidateRefusesUnusableEnvironmentEntries(t *testing.T) {
	t.Parallel()

	refused := map[string]string{
		"shell separator":      `X; id > /tmp/pwned; Y=1`,
		"command substitution": "A`id`=1",
		"dollar substitution":  "B$(id)=1",
		"no separator":         "NO_EQUALS",
		"empty name":           "=value",
		"leading digit":        "9LEADING=x",
		"embedded space":       "TWO WORDS=x",
	}

	for name, entry := range refused {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cmd := invoke.New("true")
			cmd.Env = []string{entry}

			require.Error(t, cmd.Validate(), "%q must be refused", entry)
		})
	}

	accepted := []string{"GOOD_NAME=ok", "EMPTY=", "_LEADING_UNDERSCORE=1", "MIXED123=a=b=c"}

	for _, entry := range accepted {
		cmd := invoke.New("true")
		cmd.Env = []string{entry}

		assert.NoError(t, cmd.Validate(), "%q must be accepted", entry)
	}
}
