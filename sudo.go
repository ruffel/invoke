package invoke

// sudoConfig collects sudo wrapping options.
type sudoConfig struct {
	user        string
	group       string
	preserveEnv bool
	flags       []string
}

// SudoOption configures how [WithSudo] wraps a command.
type SudoOption func(*sudoConfig)

// WithSudo runs the command through sudo in non-interactive mode
// (sudo -n), so a password prompt fails rather than hangs.
//
// The command and its arguments are passed after a -- separator as an
// argv, never as a shell string, so no argument can be misread as a sudo
// flag or interpreted by a shell.
//
// The process the target starts is sudo, so everything the provider does
// addresses sudo rather than the wrapped command:
//
//   - Start resolves sudo, and only a missing sudo fails wrapping
//     [ErrNotFound]. A wrapped command sudo cannot find, or a password
//     sudo -n will not prompt for, makes sudo exit non-zero: an
//     [ExitError], with sudo's own message on standard error, and never
//     retried.
//   - Command.Env is set on sudo's process, whichever way the provider
//     delivers it, and sudo resets the environment by default: the
//     variables do not reach the wrapped command unless the sudoers
//     policy preserves them or [WithSudoPreserveEnv] is set and
//     permitted.
//   - [Process.Signal] and Close address sudo. sudo relays the signals it
//     can catch to the command, but SIGKILL cannot be caught: a command
//     sudo runs as a child of its own is killed with it only where the
//     target signals the whole process group, as [Process.Signal]
//     describes.
func WithSudo(opts ...SudoOption) Option {
	return func(c *execConfig) {
		sc := &sudoConfig{}
		for _, opt := range opts {
			opt(sc)
		}

		c.sudo = sc
	}
}

// WithSudoUser runs the command as the given target user (sudo -u).
func WithSudoUser(user string) SudoOption {
	return func(c *sudoConfig) {
		c.user = user
	}
}

// WithSudoGroup runs the command as the given target group (sudo -g).
func WithSudoGroup(group string) SudoOption {
	return func(c *sudoConfig) {
		c.group = group
	}
}

// WithSudoPreserveEnv preserves the invoking environment (sudo -E), subject
// to the sudoers policy allowing it.
func WithSudoPreserveEnv() SudoOption {
	return func(c *sudoConfig) {
		c.preserveEnv = true
	}
}

// WithSudoFlags appends additional flags to the sudo invocation, before the
// -- separator. Callers own the safety of anything passed here.
func WithSudoFlags(flags ...string) SudoOption {
	return func(c *sudoConfig) {
		c.flags = append(c.flags, flags...)
	}
}

// applySudo rewrites cmd to run through sudo, or returns it unchanged when
// no sudo policy is configured. Arguments are passed as an argv after a --
// separator, so nothing can be misread as a flag or interpreted by a
// shell.
func applySudo(cfg execConfig, cmd Command) Command {
	if cfg.sudo == nil {
		return cmd
	}

	args := []string{"-n"}
	if cfg.sudo.user != "" {
		args = append(args, "-u", cfg.sudo.user)
	}

	if cfg.sudo.group != "" {
		args = append(args, "-g", cfg.sudo.group)
	}

	if cfg.sudo.preserveEnv {
		args = append(args, "-E")
	}

	args = append(args, cfg.sudo.flags...)
	args = append(args, "--", cmd.Path)
	args = append(args, cmd.Args...)

	return Command{Path: "sudo", Args: args, Env: cmd.Env, Dir: cmd.Dir}
}
