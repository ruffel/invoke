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

// Build returns the constructed Command.
func (b *Builder) Build() *Command {
	return b.cmd
}
