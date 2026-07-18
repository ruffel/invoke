package ssh_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/ssh"
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
			if err != nil {
				t.Fatalf("Start/Wait = %v", err)
			}

			if result.ExitCode != 0 {
				t.Fatalf("exit code = %d, want 0", result.ExitCode)
			}

			if out != arg {
				t.Errorf("argument round-tripped as %q, want %q", out, arg)
			}
		})
	}
}

// TestWorkdirSurvivesMetacharacters checks a working directory carrying
// shell metacharacters is applied as a directory, not as script.
func TestWorkdirSurvivesMetacharacters(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	dir := filepath.Join(base, "od'd $(id) dir")

	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	env := dialServer(t, startTestServer(t))

	cmd := invoke.New("pwd")
	cmd.Dir = dir

	out, result, err := runOutput(t, env, cmd)
	if err != nil {
		t.Fatalf("Start/Wait = %v", err)
	}

	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", result.ExitCode)
	}

	if strings.TrimSpace(out) != dir {
		t.Errorf("working directory = %q, want %q", strings.TrimSpace(out), dir)
	}
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
	if err != nil {
		t.Fatalf("Start/Wait = %v", err)
	}

	if result.ExitCode != 0 || strings.TrimSpace(out) != secret {
		t.Fatalf("the variable did not reach the command: out=%q exit=%d", out, result.ExitCode)
	}

	for _, line := range srv.recordedExecs() {
		if strings.Contains(line, secret) {
			t.Errorf("the secret appeared in the remote command line %q", line)
		}
	}
}

// TestMalformedEnvEntryIsIgnored checks an entry that is not KEY=VALUE is
// dropped rather than corrupting the environment or the command line.
func TestMalformedEnvEntryIsIgnored(t *testing.T) {
	t.Parallel()

	env := dialServer(t, startTestServer(t))

	cmd := invoke.New("printenv", "KEEP")
	cmd.Env = []string{"NO_EQUALS_SIGN", "KEEP=kept"}

	out, result, err := runOutput(t, env, cmd)
	if err != nil {
		t.Fatalf("Start/Wait = %v", err)
	}

	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0; a malformed entry must not break the command", result.ExitCode)
	}

	if strings.TrimSpace(out) != "kept" {
		t.Errorf("KEEP = %q, want %q", strings.TrimSpace(out), "kept")
	}
}

// TestUnexecutableFileIsNotFound checks a file that exists but cannot be
// executed is reported as an unresolvable command rather than as a
// runtime exit code.
func TestUnexecutableFileIsNotFound(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "not-executable")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho hi\n"), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	env := dialServer(t, startTestServer(t))

	_, err := env.Start(t.Context(), invoke.New(path), invoke.IO{})
	if !errors.Is(err, invoke.ErrNotFound) {
		t.Errorf("Start of a non-executable file = %v, want ErrNotFound", err)
	}
}

// TestRelativePathResolvesAgainstWorkdir checks a relative executable is
// resolved against the command's working directory, which is where the
// command itself runs — the pre-flight check must agree with it.
func TestRelativePathResolvesAgainstWorkdir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	script := filepath.Join(dir, "script.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho ran\n"), 0o700); err != nil {
		t.Fatalf("writing script: %v", err)
	}

	env := dialServer(t, startTestServer(t))

	cmd := invoke.New("./script.sh")
	cmd.Dir = dir

	out, result, err := runOutput(t, env, cmd)
	if err != nil {
		t.Fatalf("Start/Wait = %v", err)
	}

	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", result.ExitCode)
	}

	if strings.TrimSpace(out) != "ran" {
		t.Errorf("output = %q, want %q", strings.TrimSpace(out), "ran")
	}
}
