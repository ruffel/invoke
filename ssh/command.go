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
// prologue, when non-empty, carries whatever is needed to bring the
// environment into scope first; see deliverEnv.
func commandLine(cmd invoke.Command, prologue string) string {
	parts := make([]string, 0, 1+len(cmd.Args))
	parts = append(parts, quoteArg(cmd.Path))

	for _, arg := range cmd.Args {
		parts = append(parts, quoteArg(arg))
	}

	line := "exec " + strings.Join(parts, " ")
	if cmd.Dir != "" {
		line = "cd " + quoteArg(cmd.Dir) + " && " + line
	}

	return prologue + line
}

// envValue returns the last value name carries in env, honoring the
// same last-wins rule the delivered environment has.
func envValue(env []string, name string) (string, bool) {
	value, found := "", false

	for _, pair := range env {
		if key, v, ok := strings.Cut(pair, "="); ok && key == name {
			value, found = v, true
		}
	}

	return value, found
}

// exportScript renders KEY=VALUE pairs as a shell script that exports
// them, for a file the command line sources.
func exportScript(pairs []string) string {
	var script strings.Builder

	for _, pair := range pairs {
		if key, value, ok := strings.Cut(pair, "="); ok {
			script.WriteString("export " + key + "=" + quoteArg(value) + "\n")
		}
	}

	return script.String()
}

// exportPrologue renders the pairs directly onto the command line, where
// they are visible to every account on the remote host.
func exportPrologue(pairs []string) string {
	var prologue strings.Builder

	for _, pair := range pairs {
		if key, value, ok := strings.Cut(pair, "="); ok {
			prologue.WriteString("export " + key + "=" + quoteArg(value) + "; ")
		}
	}

	return prologue.String()
}

// sourcePrologue reads the environment from a file and removes it before
// the command runs, so it exists only for the moment in between.
//
// Failing to read it exits rather than running the command without its
// environment, which is the outcome this whole route exists to avoid.
// The readability test stands on its own ahead of the source: dot is a
// special built-in, and a POSIX shell aborts outright when one fails,
// skipping any || that was waiting to catch it — the guard would never
// fire from where it looks like it belongs.
func sourcePrologue(path string) string {
	quoted := quoteArg(path)

	return "[ -r " + quoted + " ] || exit " + strconv.Itoa(envDeliveryFailed) + "; " +
		". " + quoted + "; rm -f " + quoted + "; "
}

// Exit codes used by the pre-flight check to distinguish a missing working
// directory from an unresolvable command. They sit above the range a normal
// command would plausibly use for these conditions.
const (
	preCheckBadDir   = 91
	preCheckNotFound = 92

	// envDeliveryFailed reports that the environment file could not be
	// read, so the command was not run.
	//
	// The status is reserved on the file-delivery route: unlike the
	// pre-check statuses, which run in an exec of their own, this one
	// shares the command's session, so a command of its own exiting 93
	// under that route is read as a delivery failure. WithCommandLineEnv
	// avoids the file and with it the reservation.
	envDeliveryFailed = 93
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

	// The check resolves the executable exactly where the command will:
	// a PATH the caller supplies travels into the pre-check, or the two
	// disagree — refusing a runnable command, or waving through a name
	// that resolves elsewhere at run time. PATH is no secret; the
	// values that stay off command lines travel by the env machinery.
	if value, ok := envValue(cmd.Env, "PATH"); ok {
		b.WriteString("PATH=" + quoteArg(value) + "; export PATH; ")
	}

	if cmd.Dir != "" {
		b.WriteString("cd " + quoteArg(cmd.Dir) + " 2>/dev/null")
		b.WriteString(" || exit " + strconv.Itoa(preCheckBadDir) + "; ")
	}

	// A name is resolved by walking PATH for executable files, exactly
	// as every other provider resolves one. A path is checked directly,
	// because "command -v" given a path answers whether the file exists
	// on some shells and whether it can be executed on others — and the
	// first answer would let a file that cannot run reach the caller as
	// a runtime failure instead.
	b.WriteString("case " + quoteArg(cmd.Path) + " in ")
	b.WriteString("*/*) [ -f " + quoteArg(cmd.Path) + " ] && [ -x " + quoteArg(cmd.Path) + " ]")
	b.WriteString(" || exit " + strconv.Itoa(preCheckNotFound) + " ;; ")
	b.WriteString("*) " + pathWalkLine(quoteArg(cmd.Path), "exit 0", "exit "+strconv.Itoa(preCheckNotFound)) + " ;; ")
	b.WriteString("esac")

	return b.String()
}

// pathWalkLine walks the login PATH for the already-quoted name, files
// only, running hit on the first match and miss when nothing matched.
//
// The walk is what keeps resolution honest: "command -v" answers for
// shell builtins and keywords too, so it blesses a bare "cd" that no
// exec could ever run, and it shadows a real /bin/echo behind the
// builtin of the same name. Every other provider resolves through the
// filesystem; this is the same search spelled in sh.
func pathWalkLine(quoted, hit, miss string) string {
	probe := `[ -f "$d/"` + quoted + ` ] && [ -x "$d/"` + quoted + ` ]`

	return "IFS=:; for d in $PATH; do " + probe + " && { " + hit + "; }; done; " + miss
}

// lookPathLine resolves a name the way the local provider's LookPath
// does: a name containing a slash is checked directly; a bare name
// walks the login PATH and prints the first executable file it finds.
func lookPathLine(name string) string {
	quoted := quoteArg(name)

	var b strings.Builder

	b.WriteString("case " + quoted + " in ")
	b.WriteString("*/*) [ -f " + quoted + " ] && [ -x " + quoted + " ] && printf '%s\\n' " + quoted + " ;; ")
	b.WriteString("*) " + pathWalkLine(quoted, `printf '%s\n' "$d/"`+quoted+"; exit 0", "exit 1") + " ;; ")
	b.WriteString("esac")

	return b.String()
}
