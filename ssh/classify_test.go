package ssh_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPTYRefusalIsNotSupported pins the classification of a server
// saying no to terminal allocation — PermitTTY no answers the same way
// every time, so the refusal is a policy verdict, not a transport blip,
// and retrying it is futile.
func TestPTYRefusalIsNotSupported(t *testing.T) {
	t.Parallel()

	srv := startTestServer(t, withRefusedPTY())
	env := dialServer(t, srv)

	proc, err := env.Start(t.Context(), invoke.New("true"), invoke.IO{TTY: &invoke.TTY{}})
	if err == nil {
		_ = proc.Close()

		require.Fail(t, "the server refused the terminal; Start cannot have succeeded")
	}

	assert.ErrorIs(t, err, invoke.ErrNotSupported,
		"a refused terminal is a thing this server will not do")

	var transportErr *invoke.TransportError

	assert.NotErrorAs(t, err, &transportErr,
		"a deterministic policy refusal must not classify as retryable transport")
}

// TestUnknownServerOSIsReportedUnknown pins OS() against a system the
// package has never verified its assumptions on: not determined is the
// honest answer, not "probably Linux".
func TestUnknownServerOSIsReportedUnknown(t *testing.T) {
	t.Parallel()

	srv := startTestServer(t, withUnameOutput("FreeBSD"))
	env := dialServer(t, srv)

	assert.Equal(t, invoke.OSUnknown, env.OS(),
		"an unrecognized uname must not be reported as a known system")
}

// TestLookPathReportsOnlyExecutablePaths pins LookPath to its own
// promise: the answer is an executable path on the target. A shell
// builtin answers command -v and is not one; an option-shaped name must
// not be read as a flag.
func TestLookPathReportsOnlyExecutablePaths(t *testing.T) {
	t.Parallel()

	srv := startTestServer(t)
	env := dialServer(t, srv)

	// "cd" is a builtin everywhere and a real file on some systems: the
	// invariant is that the answer is never the bare builtin name.
	if resolved, err := env.LookPath(t.Context(), "cd"); err == nil {
		assert.Truef(t, strings.HasPrefix(resolved, "/"),
			"a bare builtin name is not an executable path, got %q", resolved)
	} else {
		assert.ErrorIs(t, err, invoke.ErrNotFound,
			"a builtin with no file behind it is not found")
	}

	_, err := env.LookPath(t.Context(), "-p")
	assert.ErrorIs(t, err, invoke.ErrNotFound,
		"an option-shaped name is a name, not a flag for the probe")

	// "echo" is shadowed by a builtin in every POSIX shell and still a
	// real executable; the file is the answer LookPath promised.
	resolved, err := env.LookPath(t.Context(), "echo")
	require.NoError(t, err, "a real /bin/echo must not be hidden by the builtin of the same name")

	assert.True(t, strings.HasPrefix(resolved, "/"),
		"a resolved executable is an absolute path, got %q", resolved)
}

// TestPreCheckHonorsCommandPATH pins the pre-flight check to resolving
// the executable exactly where the command will: with the PATH the
// caller supplied, not the login default. Diverging turns a runnable
// command into ErrNotFound — or waves through a name that resolves to
// something else entirely at run time.
func TestPreCheckHonorsCommandPATH(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tool := filepath.Join(dir, "custom-tool")
	require.NoError(t, os.WriteFile(tool, []byte("#!/bin/sh\necho from-custom-path\n"), 0o755))

	srv := startTestServer(t)
	env := dialServer(t, srv)

	cmd := invoke.New("custom-tool")
	cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin"}

	out, result, err := runOutput(t, env, cmd)
	require.NoError(t, err, "a command resolvable through its own PATH must start")
	require.Equal(t, 0, result.ExitCode)

	assert.Equal(t, "from-custom-path", strings.TrimSpace(out))
}
