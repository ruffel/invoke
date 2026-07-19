package invoke

import (
	"errors"
	"fmt"
	"strings"
)

// Command specifies a process to run: the executable, its arguments, its
// environment, and its working directory. It is a pure value — a Command
// carries no per-invocation state, so the same value can be started any
// number of times, sequentially or concurrently.
//
// Everything that belongs to a single invocation — streams and TTY
// allocation — lives in [IO] instead.
type Command struct {
	// Path is the executable to run: a bare name resolved by the target
	// (via its PATH or equivalent), or a path on the target.
	Path string

	// Args are passed to the executable exactly as given, with no shell
	// interpretation. Use [Shell] to run a shell script.
	Args []string

	// Env lists additional environment variables in "KEY=VALUE" form,
	// applied over the target's base environment.
	Env []string

	// Dir is the working directory on the target. Empty means the
	// provider's default for the target.
	Dir string
}

// New returns a Command that runs path with args.
func New(path string, args ...string) Command {
	return Command{Path: path, Args: args}
}

// Shell returns a Command that runs script with the target's POSIX shell,
// equivalent to: sh -c script.
//
// The script is passed to the shell verbatim: it is code, and callers are
// responsible for quoting anything interpolated into it.
func Shell(script string) Command {
	return Command{Path: "sh", Args: []string{"-c", script}}
}

// Validate returns an error if the Command cannot be started as specified.
// Providers call it before starting; callers may use it for early checks.
func (c Command) Validate() error {
	if strings.TrimSpace(c.Path) == "" {
		return errors.New("invoke: command path is empty")
	}

	for _, entry := range c.Env {
		if err := validateEnvEntry(entry); err != nil {
			return err
		}
	}

	return nil
}

// validateEnvEntry rejects an environment entry that cannot be delivered
// faithfully to every target.
//
// A name outside the portable set is refused rather than passed on. Some
// targets hand the environment to the kernel, which would accept it; one
// has to render it as shell text, where a name carrying punctuation stops
// being a name and becomes script. Accepting it where it is harmless and
// executing it where it is not is the worst of both, so the same names are
// refused everywhere.
//
// An entry with no separator is refused for a plainer reason: nothing
// consumes it. Every target discards it, so it can only ever be a mistake.
func validateEnvEntry(entry string) error {
	name, _, ok := strings.Cut(entry, "=")
	if !ok {
		return fmt.Errorf("invoke: environment entry %q is not KEY=VALUE", entry)
	}

	if name == "" {
		return fmt.Errorf("invoke: environment entry %q has an empty name", entry)
	}

	for i, r := range name {
		alpha := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if alpha || (i > 0 && r >= '0' && r <= '9') {
			continue
		}

		return fmt.Errorf(
			"invoke: environment name %q may contain only letters, digits and underscore, "+
				"and may not start with a digit", name)
	}

	return nil
}
