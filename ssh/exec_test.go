package ssh_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/ssh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runOutput starts a command and returns its stdout and result.
func runOutput(t *testing.T, env *ssh.Environment, cmd invoke.Command) (string, invoke.Result, error) {
	t.Helper()

	var out bytes.Buffer

	proc, err := env.Start(t.Context(), cmd, invoke.IO{Stdout: &out})
	if err != nil {
		return "", invoke.Result{}, err
	}

	result, waitErr := proc.Wait()

	return out.String(), result, waitErr
}

// TestArgumentsSurviveVerbatim checks the shell-quoted command line
// delivers each argument exactly as given. The SSH protocol carries a
// command string rather than an argv, so every one of these is a chance
// for the remote shell to reinterpret the caller's data.
func TestArgumentsSurviveVerbatim(t *testing.T) {
	t.Parallel()

	args := map[string]string{
		"spaces":               "two words",
		"single quote":         "it's here",
		"quote adjacent":       `'quoted'`,
		"double quote":         `say "hi"`,
		"command substitution": "$(id)",
		"backticks":            "`id`",
		"variable":             "$HOME",
		"semicolon":            "a; echo pwned",
		"pipe":                 "a | echo pwned",
		"ampersand":            "a && echo pwned",
		"newline":              "line one\nline two",
		"tab":                  "a\tb",
		"backslash":            `back\slash`,
		"glob":                 "*",
		"tilde":                "~",
		"unicode":              "héllo→world",
		"empty":                "",
		"leading dash":         "--flag=value",
	}

	env := dialServer(t, startTestServer(t))

	for label, arg := range args {
		t.Run(label, func(t *testing.T) {
			t.Parallel()

			// printf %s writes the argument with no interpretation of
			// its own, so any difference is the transport's doing.
			out, result, err := runOutput(t, env, invoke.New("printf", "%s", arg))
			require.NoError(t, err)
			require.Equal(t, 0, result.ExitCode)

			assert.Equal(t, arg, out)
		})
	}
}

// TestWorkdirSurvivesMetacharacters checks a working directory carrying
// shell metacharacters is applied as a directory, not as script.
func TestWorkdirSurvivesMetacharacters(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	dir := filepath.Join(base, "od'd $(id) dir")

	require.NoError(t, os.Mkdir(dir, 0o750), "mkdir")

	env := dialServer(t, startTestServer(t))

	cmd := invoke.New("pwd")
	cmd.Dir = dir

	out, result, err := runOutput(t, env, cmd)
	require.NoError(t, err)
	require.Equal(t, 0, result.ExitCode)

	assert.Equal(t, dir, strings.TrimSpace(out))
}

// TestEnvIsNotVisibleInTheCommandLine checks environment variables are
// delivered out of band. Anything placed on the command line is visible
// in the remote process table to every user on the host, so a secret
// passed as an environment variable must never appear there.
func TestEnvIsNotVisibleInTheCommandLine(t *testing.T) {
	t.Parallel()

	const secret = "s3cr3t-value-not-in-argv"

	srv := startTestServer(t)
	env := dialServer(t, srv)

	cmd := invoke.New("printenv", "TOKEN")
	cmd.Env = []string{"TOKEN=" + secret}

	out, result, err := runOutput(t, env, cmd)
	require.NoError(t, err)
	require.Equal(t, 0, result.ExitCode, "the variable did not reach the command")
	require.Equal(t, secret, strings.TrimSpace(out), "the variable did not reach the command")

	for _, line := range srv.recordedExecs() {
		assert.NotContains(t, line, secret, "the secret appeared in the remote command line")
	}
}

// TestHostileEnvNameIsRefusedBeforeTheCommandRuns checks a name that would
// become script when rendered for the shell never reaches it.
//
// Variables the server declines are delivered by generating shell text, so
// a name carrying punctuation would stop being a name and start being
// commands. The refusal happens before anything is started.
func TestHostileEnvNameIsRefusedBeforeTheCommandRuns(t *testing.T) {
	t.Parallel()

	env := dialServer(t, startTestServer(t))

	for _, hostile := range []string{
		`X; touch /tmp/invoke-pwned; Y=1`,
		"A`touch /tmp/invoke-pwned`=1",
		"B$(touch /tmp/invoke-pwned)=1",
	} {
		cmd := invoke.New("true")
		cmd.Env = []string{hostile}

		_, err := env.Start(t.Context(), cmd, invoke.IO{})
		require.Error(t, err, "a hostile environment name must be refused: %q", hostile)
	}

	assert.NoFileExists(t, "/tmp/invoke-pwned", "the injected command ran")
}

// TestMalformedEnvEntryIsRefused checks an entry that is not KEY=VALUE
// fails the command rather than being dropped. No target consumes such an
// entry, so accepting it would only ever hide a mistake.
func TestMalformedEnvEntryIsRefused(t *testing.T) {
	t.Parallel()

	env := dialServer(t, startTestServer(t))

	cmd := invoke.New("printenv", "KEEP")
	cmd.Env = []string{"NO_EQUALS_SIGN", "KEEP=kept"}

	_, err := env.Start(t.Context(), cmd, invoke.IO{})
	require.ErrorContains(t, err, "NO_EQUALS_SIGN", "the error must name the offending entry")
}

// TestUnexecutableFileIsNotFound checks a file that exists but cannot be
// executed is reported as an unresolvable command rather than as a
// runtime exit code.
func TestUnexecutableFileIsNotFound(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "not-executable")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\necho hi\n"), 0o600), "writing fixture")

	env := dialServer(t, startTestServer(t))

	_, err := env.Start(t.Context(), invoke.New(path), invoke.IO{})
	assert.ErrorIs(t, err, invoke.ErrNotFound, "Start of a non-executable file")
}

// TestRelativePathResolvesAgainstWorkdir checks a relative executable is
// resolved against the command's working directory, which is where the
// command itself runs — the pre-flight check must agree with it.
func TestRelativePathResolvesAgainstWorkdir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	script := filepath.Join(dir, "script.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\necho ran\n"), 0o700), "writing script")

	env := dialServer(t, startTestServer(t))

	cmd := invoke.New("./script.sh")
	cmd.Dir = dir

	out, result, err := runOutput(t, env, cmd)
	require.NoError(t, err)
	require.Equal(t, 0, result.ExitCode)

	assert.Equal(t, "ran", strings.TrimSpace(out))
}
