package invoke

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuilder_Cmd(t *testing.T) {
	t.Parallel()

	cmd := Cmd("ls").
		Arg("-l").
		Arg("-a").
		Dir("/tmp").
		Env("FOO", "bar").
		Input("some input").
		Tty().
		Build()

	assert.Equal(t, "ls", cmd.Cmd)
	assert.Equal(t, []string{"-l", "-a"}, cmd.Args)
	assert.Equal(t, "/tmp", cmd.Dir)
	assert.Equal(t, []string{"FOO=bar"}, cmd.Env)
	assert.True(t, cmd.Tty)

	// Verify input
	inputBytes, err := io.ReadAll(cmd.Stdin)
	require.NoError(t, err)
	assert.Equal(t, "some input", string(inputBytes))
}

func TestBuilder_Args(t *testing.T) {
	t.Parallel()

	cmd := Cmd("echo").
		Args("hello", "world").
		Build()

	assert.Equal(t, "echo", cmd.Cmd)
	assert.Equal(t, []string{"hello", "world"}, cmd.Args)
}

func TestBuilder_Streams(t *testing.T) {
	t.Parallel()

	var stdout, stderr strings.Builder

	cmd := Cmd("sh").
		Stdout(&stdout).
		Stderr(&stderr).
		Build()

	assert.NotNil(t, cmd.Stdout)
	assert.NotNil(t, cmd.Stderr)
}

func TestBuilder_Reuse(t *testing.T) {
	t.Parallel()

	builder := Cmd("base")
	cmd1 := builder.Arg("arg1").Build()

	// Modify the builder further
	cmd2 := builder.Arg("arg2").Env("KEY", "val").Build()

	// Verify cmd1 is unchanged
	assert.Equal(t, "base", cmd1.Cmd)
	assert.Equal(t, []string{"arg1"}, cmd1.Args)
	assert.Empty(t, cmd1.Env)

	// Verify cmd2 has all changes
	assert.Equal(t, "base", cmd2.Cmd)
	assert.Equal(t, []string{"arg1", "arg2"}, cmd2.Args)
	assert.Equal(t, []string{"KEY=val"}, cmd2.Env)
}
