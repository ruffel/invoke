package ssh_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestNonZeroExitSurvivesConcurrentCancel covers the half of cancellation
// attribution the contract cannot reach: the contract pins a clean exit,
// and a status the server reported is authoritative whatever it says.
func TestNonZeroExitSurvivesConcurrentCancel(t *testing.T) {
	t.Parallel()

	env := dialServer(t, startTestServer(t))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	release := make(chan struct{})
	written := make(chan struct{})

	stdout := writerFunc(func(p []byte) (int, error) {
		close(written)
		<-release

		return len(p), nil
	})

	proc, err := env.Start(ctx, invoke.Shell("echo out; exit 9"), invoke.IO{Stdout: stdout})
	require.NoError(t, err)

	<-written

	// The command has produced its output and is exiting; cancel while the
	// provider is still held inside the drain.
	time.Sleep(250 * time.Millisecond)
	cancel()
	time.Sleep(250 * time.Millisecond)
	close(release)

	result, waitErr := proc.Wait()

	var exitErr *invoke.ExitError

	require.ErrorAs(t, waitErr, &exitErr,
		"the server reported exit 9 before the cancellation; that status must survive it")

	assert.Equal(t, 9, exitErr.Code, "the reported exit status was rewritten")
	assert.Equal(t, 9, result.ExitCode, "the reported exit code was rewritten")
}

// writerFunc adapts a function to io.Writer.
type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// deliveredEnvFile digs the delivery file's path out of the recorded
// execs, so a test can check what became of it on the host the test
// server runs commands on.
func deliveredEnvFile(t *testing.T, srv *testServer) string {
	t.Helper()

	for _, line := range srv.recordedExecs() {
		if m := envFilePattern.FindString(line); m != "" {
			return m
		}
	}

	t.Fatal("no delivery file appears in the recorded execs")

	return ""
}

// TestFileEnvRouteDeliversAndCleansUp runs the file-based delivery route
// end to end against a server that refuses env requests — the stock
// sshd posture — and holds it to both halves of its promise: the value
// arrives, and the file is gone before the command runs.
func TestFileEnvRouteDeliversAndCleansUp(t *testing.T) {
	t.Parallel()

	const secret = "file-route-value"

	srv := startTestServer(t, withRefusedEnv())
	env := dialServer(t, srv)

	cmd := invoke.New("printenv", "TOKEN")
	cmd.Env = []string{"TOKEN=" + secret}

	out, result, err := runOutput(t, env, cmd)
	require.NoError(t, err)
	require.Equal(t, 0, result.ExitCode, "the variable did not reach the command")
	assert.Equal(t, secret, strings.TrimSpace(out), "the variable did not reach the command")

	_, statErr := os.Stat(deliveredEnvFile(t, srv))
	assert.True(t, os.IsNotExist(statErr),
		"the delivery file must be gone before the command runs")

	for _, line := range srv.recordedExecs() {
		assert.NotContains(t, line, secret, "the secret appeared in a remote command line")
	}
}

// TestFileEnvGuardReportsAnUndeliveredEnvironment pins the route's
// failure half. When the delivery file cannot be read — a tmp cleaner
// got there first — the command must not run without its environment,
// and the caller must hear a delivery failure rather than a verdict
// from a command that never started.
func TestFileEnvGuardReportsAnUndeliveredEnvironment(t *testing.T) {
	t.Parallel()

	srv := startTestServer(t, withRefusedEnv(), withSabotagedEnvFile())
	env := dialServer(t, srv)

	marker := filepath.Join(t.TempDir(), "ran")

	cmd := invoke.New("touch", marker)
	cmd.Env = []string{"TOKEN=never-delivered"}

	_, _, err := runOutput(t, env, cmd)
	require.Error(t, err, "an undelivered environment must not read as success")

	var transportErr *invoke.TransportError

	assert.ErrorAs(t, err, &transportErr,
		"the command never ran, so the failure is retryable transport, not a verdict")

	var exitErr *invoke.ExitError

	assert.NotErrorAs(t, err, &exitErr,
		"a delivery failure must not be dressed as the command's own exit")

	_, statErr := os.Stat(marker)
	assert.True(t, os.IsNotExist(statErr),
		"the command ran without the environment it was promised")
}

// TestExitStatusReservedByTheGuardStaysAnExitElsewhere pins the
// reservation's scope: only the file route reads exit 93 as a delivery
// failure. A command exiting 93 without one is its own verdict.
func TestExitStatusReservedByTheGuardStaysAnExitElsewhere(t *testing.T) {
	t.Parallel()

	srv := startTestServer(t)
	env := dialServer(t, srv)

	_, result, err := runOutput(t, env, invoke.Shell("exit 93"))

	var exitErr *invoke.ExitError

	require.ErrorAs(t, err, &exitErr, "exit 93 without file delivery is the command's own")
	assert.Equal(t, 93, exitErr.Code)
	assert.Equal(t, 93, result.ExitCode)
}

// TestFileEnvIsRemovedWhenTheCommandCannotStart pins the orphan half:
// a delivery file written for a command whose exec the server then
// refuses must not outlive the failure.
func TestFileEnvIsRemovedWhenTheCommandCannotStart(t *testing.T) {
	t.Parallel()

	srv := startTestServer(t, withRefusedEnv(), withRefusedEnvCommandExec())
	env := dialServer(t, srv)

	cmd := invoke.New("true")
	cmd.Env = []string{"TOKEN=orphaned"}

	proc, err := env.Start(t.Context(), cmd, invoke.IO{})
	if err == nil {
		_ = proc.Close()

		require.Fail(t, "the server refused the exec; Start cannot have succeeded")
	}

	var transportErr *invoke.TransportError

	assert.ErrorAs(t, err, &transportErr, "a refused exec is the transport's failure")

	_, statErr := os.Stat(deliveredEnvFile(t, srv))
	assert.True(t, os.IsNotExist(statErr),
		"values the caller deemed too sensitive for an argv must not persist in /tmp")
}
