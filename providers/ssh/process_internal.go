package ssh

import (
	"fmt"
	"strings"

	"github.com/ruffel/invoke"
	"golang.org/x/crypto/ssh"
)

// buildEnvPrefix constructs the environment variable prefix for SSH commands.
// Since OpenSSH defaults PermitUserEnvironment=no, session.Setenv() won't work.
// We work around by prepending "export VAR='val';" to the command string.
func buildEnvPrefix(envVars []string) string {
	var envPrefix strings.Builder

	for _, env := range envVars {
		// KEY=VALUE -> KEY='VALUE' (with single quotes inside VALUE escaped)
		k, v, found := strings.Cut(env, "=")
		if !found {
			continue // Skip malformed env
		}

		// Escape single quotes in value:  ' -> '\''
		escapedV := strings.ReplaceAll(v, "'", "'\\''")

		// POSIX: export KEY='VALUE';
		fmt.Fprintf(&envPrefix, "export %s='%s'; ", k, escapedV)
	}

	return envPrefix.String()
}

// buildDirPrefix constructs the directory change prefix for SSH commands.
func buildDirPrefix(dir string) string {
	if dir == "" {
		return ""
	}
	// Escape the directory path to avoid injection/issues
	escapedDir := strings.ReplaceAll(dir, "'", "'\\''")
	// POSIX: cd 'path' &&
	return fmt.Sprintf("cd '%s' && ", escapedDir)
}

// buildTerminalModes returns the default terminal modes for a PTY.
func buildTerminalModes() ssh.TerminalModes {
	return ssh.TerminalModes{
		ssh.ECHO:          1,     // enable echoing
		ssh.TTY_OP_ISPEED: 14400, // input speed = 14.4kbaud
		ssh.TTY_OP_OSPEED: 14400, // output speed = 14.4kbaud
	}
}

// buildFullCommand constructs the complete command string for execution on the remote server.
// It combines environment variables, working directory change, and the command itself.
func buildFullCommand(cmd *invoke.Command) string {
	var sb strings.Builder
	sb.WriteString(buildEnvPrefix(cmd.Env))
	sb.WriteString(buildDirPrefix(cmd.Dir))
	sb.WriteString(quoteArg(cmd.Cmd))

	for _, arg := range cmd.Args {
		sb.WriteString(" ")
		sb.WriteString(quoteArg(arg))
	}

	return sb.String()
}

// quoteArg quotes a single argument for the remote shell.
// POSIX quoting: 'value' where ' is escaped as '\”.
func quoteArg(arg string) string {
	return "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
}
