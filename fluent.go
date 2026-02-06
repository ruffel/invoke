package invoke

import (
	"io"
	"strings"
)

// Builder provides a fluent API for constructing Commands.
type Builder struct {
	cmd *Command
}

// Cmd creates a new Builder for a command with the given name/path.
func Cmd(binary string) *Builder {
	return &Builder{
		cmd: &Command{
			Cmd: binary,
		},
	}
}

// Arg adds a single argument.
func (b *Builder) Arg(arg string) *Builder {
	b.cmd.Args = append(b.cmd.Args, arg)

	return b
}

// Args adds multiple arguments.
func (b *Builder) Args(args ...string) *Builder {
	b.cmd.Args = append(b.cmd.Args, args...)

	return b
}

// Env adds an environment variable in "KEY=VALUE" format.
func (b *Builder) Env(key, value string) *Builder {
	b.cmd.Env = append(b.cmd.Env, key+"="+value)

	return b
}

// Dir sets the working directory.
func (b *Builder) Dir(dir string) *Builder {
	b.cmd.Dir = dir

	return b
}

// Stdin sets the standard input stream.
func (b *Builder) Stdin(r io.Reader) *Builder {
	b.cmd.Stdin = r

	return b
}

// Input sets the standard input from a string.
func (b *Builder) Input(s string) *Builder {
	b.cmd.Stdin = strings.NewReader(s)

	return b
}

// Stdout sets the standard output stream.
func (b *Builder) Stdout(w io.Writer) *Builder {
	b.cmd.Stdout = w

	return b
}

// Stderr sets the standard error stream.
func (b *Builder) Stderr(w io.Writer) *Builder {
	b.cmd.Stderr = w

	return b
}

// Tty enables PTY allocation.
func (b *Builder) Tty() *Builder {
	b.cmd.Tty = true

	return b
}

// Build returns a deep copy of the constructed Command.
// The returned command is safe to use while the builder continues to be modified.
func (b *Builder) Build() *Command {
	// Deep copy the command to avoid sharing state if the builder is reused
	cmd := *b.cmd

	if len(b.cmd.Args) > 0 {
		cmd.Args = make([]string, len(b.cmd.Args))
		copy(cmd.Args, b.cmd.Args)
	}

	if len(b.cmd.Env) > 0 {
		cmd.Env = make([]string, len(b.cmd.Env))
		copy(cmd.Env, b.cmd.Env)
	}

	return &cmd
}
