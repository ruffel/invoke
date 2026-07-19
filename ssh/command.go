package ssh

import (
	"strconv"
	"strings"

	"github.com/ruffel/invoke"
)

// quoteArg renders s as a single literal word for a POSIX shell by wrapping
// it in single quotes and escaping any embedded single quote. Single quotes
// disable every form of shell interpretation, so spaces, metacharacters,
// and newlines all survive verbatim.
func quoteArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// commandLine renders a Command as the remote shell command line. The
// executable and its arguments are each quoted, so nothing is reinterpreted
// by the remote shell; exec replaces that shell with the command so signals
// and exit status pass straight through. A working directory is applied as a
// preceding cd. Environment variables are delivered out of band (via the
// session), not here, so they never appear in the remote process table.
//
// inlineEnv is the exception: variables the server refused to accept out
// of band, which the caller has explicitly opted into carrying here
// instead, where every account on the remote host can read them.
func commandLine(cmd invoke.Command, inlineEnv []string) string {
	parts := make([]string, 0, 1+len(cmd.Args))
	parts = append(parts, quoteArg(cmd.Path))

	for _, arg := range cmd.Args {
		parts = append(parts, quoteArg(arg))
	}

	line := "exec " + strings.Join(parts, " ")
	if cmd.Dir != "" {
		line = "cd " + quoteArg(cmd.Dir) + " && " + line
	}

	if len(inlineEnv) > 0 {
		var exports strings.Builder

		for _, pair := range inlineEnv {
			key, value, ok := strings.Cut(pair, "=")
			if !ok {
				continue
			}

			exports.WriteString("export " + key + "=" + quoteArg(value) + "; ")
		}

		line = exports.String() + line
	}

	return line
}

// Exit codes used by the pre-flight check to distinguish a missing working
// directory from an unresolvable command. They sit above the range a normal
// command would plausibly use for these conditions.
const (
	preCheckBadDir   = 91
	preCheckNotFound = 92
)

// preCheckLine builds a command that validates a command's working
// directory and executable before the real command runs, so those setup
// failures are reported distinctly rather than as an exit code.
//
// The check enters the working directory first, exactly as the command
// itself does. Resolving the executable from anywhere else would disagree
// with where it actually runs, and would report a relative path as
// missing when it is present. Entering the directory also covers a
// directory that exists but cannot be used, which a type test alone would
// let through.
func preCheckLine(cmd invoke.Command) string {
	var b strings.Builder

	if cmd.Dir != "" {
		b.WriteString("cd " + quoteArg(cmd.Dir) + " 2>/dev/null")
		b.WriteString(" || exit " + strconv.Itoa(preCheckBadDir) + "; ")
	}

	b.WriteString("command -v " + quoteArg(cmd.Path) + " >/dev/null 2>&1")
	b.WriteString(" || exit " + strconv.Itoa(preCheckNotFound))

	return b.String()
}
