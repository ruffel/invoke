package fake

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSupportedScriptsAreAccepted pins the subset the shell does run.
//
// Every one of these is either used by the contract suite or is the
// ordinary way to write something the shell interprets, so a check that
// refused any of them would have made the fake useless rather than
// honest.
func TestSupportedScriptsAreAccepted(t *testing.T) {
	t.Parallel()

	scripts := []string{
		"echo hi",
		"echo out; echo err 1>&2",
		"echo to-stderr >&2",
		"dd if=/dev/zero bs=1024 count=256 2>/dev/null; dd if=/dev/zero bs=1024 count=64 1>&2 2>/dev/null",
		`printf '%s|%s' "$HOME" "$INVOKE_DUP"`,
		`test -n "$(find /x -maxdepth 0 -perm 0644)"`,
		"sleep 1 && touch /marker",
		"cd /tmp",
		"echo $HOME",
		"exit 3",
		// The braced spelling of a plain name is the same expansion.
		"echo ${HOME}",
		`printf '%s' "${HOME}/bin"`,
		// A space before the target is the common spelling of the same
		// redirection.
		"echo hi > /dev/null",
		"echo hi 2> /dev/null",
		"echo hi 1> /dev/null",
		// A metacharacter inside quotes is data, not syntax.
		"echo 'a | b'",
		`echo "a > b"`,
		`echo 'back ` + "`" + `tick'`,
		"echo '*'",
		"echo 'a # b'",
		`echo 'a\b'`,
		// A # or a glob character inside a word is ordinary text.
		"echo a#b",
		// A clean substitution inside double quotes runs.
		`echo "$(echo ok)"`,
		// A newline that only trails the script separates nothing.
		"echo hi\n",
	}

	for _, script := range scripts {
		assert.NoErrorf(t, unsupportedSyntax(script), "script must be accepted: %q", script)
	}
}

// TestUnsupportedScriptsAreRefused pins the constructs the shell cannot
// run and used to take literally instead.
//
// Each of these produced a wrong answer silently: the pipeline became
// arguments, the redirection became words, and the || list inverted —
// exiting non-zero having printed nothing where a real shell exits zero
// having printed.
func TestUnsupportedScriptsAreRefused(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		script string
		names  string
	}{
		{name: "pipeline", script: "echo hi | tr h H", names: "pipeline"},
		{name: "or list", script: "false || echo rescued", names: "||"},
		{name: "redirect to a file", script: "echo data > /tmp/f.txt", names: "redirection"},
		{name: "redirect to a named file", script: "echo data >/tmp/f.txt", names: "redirection"},
		{name: "input redirect", script: "cat < /etc/hosts", names: "input redirection"},
		{name: "newline separator", script: "echo a\necho b", names: "newline"},
		{name: "status parameter", script: "false; echo $?", names: "$?"},
		{name: "positional parameter", script: "echo $1", names: "$1"},
		{name: "pid parameter", script: "echo $$", names: "$$"},
		{name: "background", script: "sleep 30 & echo started", names: "background"},
		{name: "backquote", script: "echo `date`", names: "backquote"},
		{name: "brace operator", script: "echo ${X:-default}", names: "${X:-default}"},
		{name: "empty brace", script: "echo ${}", names: "${}"},
		{name: "unclosed brace", script: "echo ${X", names: "unclosed"},
		{name: "arithmetic", script: "echo $((1+2))", names: "arithmetic"},
		{name: "leading comment", script: "# provision the host", names: "comment"},
		{name: "trailing comment", script: "echo hi # explain why", names: "comment"},
		{name: "comment after separator", script: "echo hi;# explain why", names: "comment"},
		{name: "star glob", script: "rm /tmp/*", names: "glob"},
		{name: "question glob", script: "echo a?", names: "glob"},
		{name: "backslash escape", script: `echo hello\ world`, names: "backslash"},
		{name: "backslash in double quotes", script: `echo "a\"b"`, names: "backslash"},
		{name: "pipeline inside a quoted substitution", script: `echo "$(echo hi | tr h H)"`, names: "pipeline"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := unsupportedSyntax(tt.script)
			require.Errorf(t, err, "script must be refused: %q", tt.script)

			assert.Containsf(t, err.Error(), tt.names,
				"the refusal must name what it could not run, for %q", tt.script)
		})
	}
}
