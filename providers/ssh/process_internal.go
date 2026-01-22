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
func buildEnvPrefix(envVars []string, isWindows bool) string {
	var envPrefix strings.Builder

	for _, env := range envVars {
		// KEY=VALUE -> KEY='VALUE' (with single quotes inside VALUE escaped)
		k, v, found := strings.Cut(env, "=")
		if !found {
			continue // Skip malformed env
		}

		// Escape single quotes in value:  ' -> '\''
		escapedV := strings.ReplaceAll(v, "'", "'\\''")

		if isWindows {
			// PowerShell: $env:KEY='VALUE';
			fmt.Fprintf(&envPrefix, "$env:%s='%s'; ", k, escapedV)
		} else {
			// POSIX: export KEY='VALUE';
			fmt.Fprintf(&envPrefix, "export %s='%s'; ", k, escapedV)
		}
	}

	return envPrefix.String()
}

// buildDirPrefix constructs the directory change prefix for SSH commands.
func buildDirPrefix(dir string, isWindows bool) string {
	if dir == "" {
		return ""
	}
	// Escape the directory path to avoid injection/issues
	escapedDir := strings.ReplaceAll(dir, "'", "'\\''")
	if isWindows {
		// Windows: cd 'path';
		return fmt.Sprintf("cd '%s'; ", escapedDir)
	}
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

// buildFullCommand constructs the complete command string to execution on the remote server.
// It combines environment variables, working directory change, and the command itself.
func buildFullCommand(cmd *invoke.Command, isWindows bool) string {
	return buildEnvPrefix(cmd.Env, isWindows) + buildDirPrefix(cmd.Dir, isWindows) + cmd.String()
}
