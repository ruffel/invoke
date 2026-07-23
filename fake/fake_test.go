package fake_test

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/fake"
	"github.com/ruffel/invoke/invoketest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFakePassesContractSuite is the fake's flagship property: it passes
// the same behavioral contracts real providers pass, so consumer tests
// built on it inherit contract-accurate machinery.
func TestFakePassesContractSuite(t *testing.T) {
	t.Parallel()

	invoketest.Verify(t, func(_ invoketest.T) invoke.Environment {
		return fake.New()
	})
}

func TestHandlersOverrideBuiltinsAndRecordCalls(t *testing.T) {
	t.Parallel()

	env := fake.New()

	t.Cleanup(func() { _ = env.Close() })

	env.Handle("deploy", func(_ context.Context, cmd invoke.Command, s *fake.Session) int {
		input, _ := io.ReadAll(s.Stdin)

		_, _ = io.WriteString(s.Stdout, "deployed "+cmd.Args[0]+" with "+string(input))
		_, _ = io.WriteString(s.Stderr, "warning: simulated\n")

		return 0
	})

	var stdout, stderr bytes.Buffer

	proc, err := env.Start(t.Context(), invoke.New("deploy", "api", "--fast"), invoke.IO{
		Stdin:  strings.NewReader("config"),
		Stdout: &stdout,
		Stderr: &stderr,
	})
	require.NoError(t, err)

	res, err := proc.Wait()
	require.NoError(t, err, "want success")
	require.Equal(t, 0, res.ExitCode, "want success")

	assert.Equal(t, "deployed api with config", stdout.String(), "stdout")
	assert.Equal(t, "warning: simulated\n", stderr.String(), "stderr")

	calls := env.Calls()
	require.Len(t, calls, 1, "want the deploy invocation recorded")
	assert.Equal(t, "deploy", calls[0].Path, "want the deploy invocation recorded")
	assert.Equal(t, "--fast", calls[0].Args[1], "want the deploy invocation recorded")
}

// TestNamesResolveConsistently pins one rule across Start, LookPath and
// the shell: a name the fake answers for is answerable everywhere it
// can be written. A handler callable by Start but unknown to a script,
// or a LookPath answer Start then refuses, is the fake disagreeing with
// itself — and both patterns work on every real target.
func TestNamesResolveConsistently(t *testing.T) {
	t.Parallel()

	newEnv := func(t *testing.T) *fake.Environment {
		t.Helper()

		env := fake.New()

		t.Cleanup(func() { _ = env.Close() })

		env.Handle("deploy", func(_ context.Context, _ invoke.Command, s *fake.Session) int {
			_, _ = io.WriteString(s.Stdout, "deployed\n")

			return 0
		})

		return env
	}

	runScript := func(t *testing.T, env *fake.Environment, script string) (invoke.Result, string, error) {
		t.Helper()

		var stdout bytes.Buffer

		proc, err := env.Start(t.Context(), invoke.Shell(script), invoke.IO{Stdout: &stdout})
		require.NoError(t, err, "the script must start")

		res, waitErr := proc.Wait()

		return res, stdout.String(), waitErr
	}

	t.Run("scripts reach registered handlers", func(t *testing.T) {
		t.Parallel()

		env := newEnv(t)

		_, stdout, err := runScript(t, env, "deploy && echo done")
		require.NoError(t, err)

		assert.Equal(t, "deployed\ndone\n", stdout)
	})

	t.Run("substitutions reach registered handlers", func(t *testing.T) {
		t.Parallel()

		env := newEnv(t)

		_, stdout, err := runScript(t, env, `echo "$(deploy)"`)
		require.NoError(t, err)

		assert.Equal(t, "deployed\n", stdout)
	})

	t.Run("a failing handler fails the script", func(t *testing.T) {
		t.Parallel()

		env := newEnv(t)
		env.Handle("deploy", func(_ context.Context, _ invoke.Command, _ *fake.Session) int {
			return 3
		})

		res, stdout, err := runScript(t, env, "deploy && echo done")

		var exitErr *invoke.ExitError

		require.ErrorAs(t, err, &exitErr, "the handler's failure is the script's failure")
		assert.Equal(t, 3, res.ExitCode)
		assert.Empty(t, stdout, "&& must not run its right side")
	})

	t.Run("handlers override builtins in scripts", func(t *testing.T) {
		t.Parallel()

		env := newEnv(t)
		env.Handle("echo", func(_ context.Context, _ invoke.Command, s *fake.Session) int {
			_, _ = io.WriteString(s.Stdout, "handled\n")

			return 0
		})

		_, stdout, err := runScript(t, env, "echo native")
		require.NoError(t, err)

		assert.Equal(t, "handled\n", stdout, "Handle overrides a builtin in scripts as it does at Start")
	})

	t.Run("LookPath answers are startable", func(t *testing.T) {
		t.Parallel()

		env := newEnv(t)

		for _, name := range []string{"echo", "deploy"} {
			resolved, err := env.LookPath(t.Context(), name)
			require.NoError(t, err, "LookPath(%q)", name)

			var stdout bytes.Buffer

			proc, err := env.Start(t.Context(), invoke.New(resolved), invoke.IO{Stdout: &stdout})
			require.NoErrorf(t, err, "Start must accept LookPath's own answer %q", resolved)

			_, err = proc.Wait()
			require.NoError(t, err)
		}
	})

	t.Run("the conventional path runs in scripts", func(t *testing.T) {
		t.Parallel()

		env := newEnv(t)

		_, stdout, err := runScript(t, env, "/usr/bin/echo hi")
		require.NoError(t, err)

		assert.Equal(t, "hi\n", stdout)
	})

	t.Run("unknown names still fail everywhere", func(t *testing.T) {
		t.Parallel()

		env := newEnv(t)

		_, err := env.LookPath(t.Context(), "nope")
		assert.ErrorIs(t, err, invoke.ErrNotFound)

		_, err = env.Start(t.Context(), invoke.New("/usr/bin/nope"), invoke.IO{})
		assert.ErrorIs(t, err, invoke.ErrNotFound)

		res, _, waitErr := runScript(t, env, "nope")
		require.Error(t, waitErr)
		assert.Equal(t, 127, res.ExitCode, "a script's unknown command stays the shell's 127")
	})
}

func TestHandlerNonZeroExitIsExitError(t *testing.T) {
	t.Parallel()

	env := fake.New()

	t.Cleanup(func() { _ = env.Close() })

	env.Handle("flaky", func(_ context.Context, _ invoke.Command, _ *fake.Session) int {
		return 3
	})

	proc, err := env.Start(t.Context(), invoke.New("flaky"), invoke.IO{})
	require.NoError(t, err)

	res, waitErr := proc.Wait()

	var exitErr *invoke.ExitError

	require.ErrorAs(t, waitErr, &exitErr, "want ExitError code 3")
	assert.Equal(t, 3, exitErr.Code, "want ExitError code 3")
	assert.Equal(t, 3, res.ExitCode, "want ExitError code 3")
}

func TestHandlerHonoringCancellationClassifiesAsCancel(t *testing.T) {
	t.Parallel()

	env := fake.New()

	t.Cleanup(func() { _ = env.Close() })

	env.Handle("server", func(ctx context.Context, _ invoke.Command, _ *fake.Session) int {
		<-ctx.Done()

		return -1
	})

	ctx, cancel := context.WithCancel(t.Context())

	proc, err := env.Start(ctx, invoke.New("server"), invoke.IO{})
	require.NoError(t, err)

	cancel()

	_, waitErr := proc.Wait()
	assert.ErrorIs(t, waitErr, context.Canceled)
}

func TestWithEnvSeedsBaseEnvironment(t *testing.T) {
	t.Parallel()

	env := fake.New(fake.WithEnv("REGION=eu-west-1"))

	t.Cleanup(func() { _ = env.Close() })

	var stdout bytes.Buffer

	proc, err := env.Start(t.Context(), invoke.Shell(`printf '%s' "$REGION"`), invoke.IO{Stdout: &stdout})
	require.NoError(t, err)

	_, err = proc.Wait()
	require.NoError(t, err)

	assert.Equal(t, "eu-west-1", stdout.String(), "$REGION")
}

func TestFSViewExposesTargetState(t *testing.T) {
	t.Parallel()

	env := fake.New()

	t.Cleanup(func() { _ = env.Close() })

	srcDir := t.TempDir()

	require.NoError(t,
		os.WriteFile(filepath.Join(srcDir, "config.json"), []byte(`{"ok":true}`), 0o644), "fixture")
	require.NoError(t, os.Symlink("config.json", filepath.Join(srcDir, "current.json")), "symlink")
	require.NoError(t, env.Upload(t.Context(), srcDir, "/etc/app"))

	view := env.FS()

	// The adapter must satisfy the stdlib's own conformance test.
	require.NoError(t, fstest.TestFS(view, "etc/app/config.json", "etc/app/current.json"))

	content, err := fs.ReadFile(view, "etc/app/config.json")
	assert.NoError(t, err, "reading through the FS view")
	assert.JSONEq(t, `{"ok":true}`, string(content), "the FS view must expose the target's file content")

	linkFS, ok := view.(fs.ReadLinkFS)
	require.True(t, ok, "FS view does not implement fs.ReadLinkFS")

	target, err := linkFS.ReadLink("etc/app/current.json")
	assert.NoError(t, err, "reading a link through the FS view")
	assert.Equal(t, "config.json", target, "the FS view must expose the link's target")
}

func TestUnknownCommandIsNotFound(t *testing.T) {
	t.Parallel()

	env := fake.New()

	t.Cleanup(func() { _ = env.Close() })

	_, err := env.Start(t.Context(), invoke.New("unscripted-command"), invoke.IO{})
	assert.ErrorIs(t, err, invoke.ErrNotFound)
}

// TestUnsupportedShellSyntaxIsRefusedNotRun pins the shape of the
// refusal, which matters as much as the refusal itself.
//
// A script the fake shell cannot run is refused before a process exists,
// wrapping ErrNotSupported. Reporting it as a command that ran and failed
// would satisfy a caller asserting failure — and such a caller is exactly
// the one being misled, since `false || echo rescued` exits zero on every
// real target.
func TestUnsupportedShellSyntaxIsRefusedNotRun(t *testing.T) {
	t.Parallel()

	env := fake.New()

	t.Cleanup(func() { _ = env.Close() })

	proc, err := env.Start(t.Context(), invoke.Shell("false || echo rescued"), invoke.IO{})
	if err == nil {
		_ = proc.Close()

		require.Fail(t, "a script the fake shell cannot run reported success")
	}

	assert.ErrorIs(t, err, invoke.ErrNotSupported,
		"an unrunnable script is a thing this target cannot do")

	var exitErr *invoke.ExitError

	assert.NotErrorAs(t, err, &exitErr,
		"refusing a script must not look like the script running and failing")

	assert.Contains(t, err.Error(), "||", "the refusal must name what it could not run")
}

// TestBuiltinsAnswerTruthfullyOrRefuse pins the builtins' failure modes
// against what a real shell's utilities do. A silent wrong answer —
// test reporting false for a form it never evaluated, mkdir agreeing
// about a directory it did not create, rm agreeing about a path that
// was never there — reads as a verdict, and it would be a made-up one.
func TestBuiltinsAnswerTruthfullyOrRefuse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		script     string
		env        []string
		wantExit   int
		wantStdout string
		inStderr   string
	}{
		{name: "test -z on empty is true", script: `test -z ""`, wantExit: 0},
		{name: "test -z on text is false", script: `test -z x`, wantExit: 1},
		{name: "test one argument is its non-emptiness", script: `test abc`, wantExit: 0},
		{name: "test one empty argument is false", script: `test ""`, wantExit: 1},
		{name: "test refuses a binary form", script: `test a = a`, wantExit: 2, inStderr: "simulated"},
		{name: "test refuses a numeric form", script: `test 1 -eq 1`, wantExit: 2, inStderr: "simulated"},
		{name: "test refuses an unknown operator", script: `test -x /tmp`, wantExit: 2, inStderr: "simulated"},
		{name: "mkdir fails on an existing path", script: `mkdir /tmp`, wantExit: 1, inStderr: "file exists"},
		{name: "mkdir fails on a missing parent", script: `mkdir /a/b`, wantExit: 1, inStderr: "no such file or directory"},
		{name: "mkdir -p tolerates both", script: `mkdir -p /a/b && mkdir -p /a/b`, wantExit: 0},
		{name: "mkdir still creates", script: `mkdir /fresh && test -d /fresh`, wantExit: 0},
		{name: "rm without an operand fails", script: `rm`, wantExit: 1, inStderr: "missing operand"},
		{name: "rm fails on a missing path", script: `rm /missing`, wantExit: 1, inStderr: "no such file or directory"},
		{name: "rm -f tolerates a missing path", script: `rm -f /missing`, wantExit: 0},
		{name: "rm still removes", script: `touch /f && rm /f && test -e /f`, wantExit: 1},
		{name: "exit refuses a non-numeric status", script: `exit abc`, wantExit: 2, inStderr: "numeric argument required"},
		{
			name:       "cd alone goes home",
			script:     `mkdir -p /home/dev && cd && pwd`,
			env:        []string{"HOME=/home/dev"},
			wantExit:   0,
			wantStdout: "/home/dev\n",
		},
		{
			name:     "cd alone without HOME fails",
			script:   `cd`,
			env:      []string{"HOME="},
			wantExit: 1,
			inStderr: "HOME not set",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := fake.New(fake.WithEnv(tt.env...))

			t.Cleanup(func() { _ = env.Close() })

			var stdout, stderr bytes.Buffer

			proc, err := env.Start(t.Context(), invoke.Shell(tt.script), invoke.IO{Stdout: &stdout, Stderr: &stderr})
			require.NoError(t, err, "every script here is within the subset")

			res, waitErr := proc.Wait()
			assert.Equal(t, tt.wantExit, res.ExitCode, "exit code must match a real shell's")

			if tt.wantExit == 0 {
				assert.NoError(t, waitErr)
			} else {
				var exitErr *invoke.ExitError

				assert.ErrorAs(t, waitErr, &exitErr, "a non-zero answer is an ExitError, not a refusal")
			}

			assert.Equal(t, tt.wantStdout, stdout.String(), "stdout")

			if tt.inStderr != "" {
				assert.Contains(t, stderr.String(), tt.inStderr,
					"the failure must say what went wrong")
			}
		})
	}
}

// TestBracedNamesAndSpacedRedirectsRun pins the two spellings the shell
// accepts as part of its subset: ${NAME} is the same expansion as
// $NAME, and a space before /dev/null is the same redirection as the
// flush form. Accepting either at Start and then mishandling it would
// be the silent wrong answer the refusal machinery exists to prevent.
func TestBracedNamesAndSpacedRedirectsRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		script     string
		wantStdout string
		wantStderr string
	}{
		{
			name:       "braced name expands",
			script:     `echo ${REGION}`,
			wantStdout: "eu-west-1\n",
		},
		{
			name:       "braced name expands inside double quotes",
			script:     `printf '%s' "${REGION}/vm"`,
			wantStdout: "eu-west-1/vm",
		},
		{
			name:       "spaced null redirect discards stdout",
			script:     `echo hi > /dev/null`,
			wantStdout: "",
		},
		{
			name:       "spaced null redirect discards the named stream",
			script:     `echo hi 1> /dev/null`,
			wantStdout: "",
		},
		{
			name:       "a hash inside a word is data",
			script:     `echo a#b`,
			wantStdout: "a#b\n",
		},
		{
			name:       "a quoted glob character is data",
			script:     `echo '*'`,
			wantStdout: "*\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := fake.New(fake.WithEnv("REGION=eu-west-1"))

			t.Cleanup(func() { _ = env.Close() })

			var stdout, stderr bytes.Buffer

			proc, err := env.Start(t.Context(), invoke.Shell(tt.script), invoke.IO{Stdout: &stdout, Stderr: &stderr})
			require.NoError(t, err, "every script here is within the subset")

			res, waitErr := proc.Wait()
			require.NoError(t, waitErr, "want exit 0")
			require.Equal(t, 0, res.ExitCode, "want exit 0")

			assert.Equal(t, tt.wantStdout, stdout.String(),
				"stdout must match what a real shell produces")
			assert.Equal(t, tt.wantStderr, stderr.String(),
				"stderr must match what a real shell produces")
		})
	}
}

// TestQuotedMetacharactersAreDataAtRuntime pins what happens after
// acceptance. The check at Start already lets a quoted metacharacter
// through as data; the tokenizer must then keep treating it as data. A
// quoted "2>&1" that rewires streams is the shell answering wrongly
// without saying so — on every real target it is a plain argument.
//
// The same rule covers values that arrive by expansion: a variable or
// substitution result is data wherever it lands, never syntax.
func TestQuotedMetacharactersAreDataAtRuntime(t *testing.T) {
	t.Parallel()

	envCmd := invoke.Shell(`echo $REDIR`)
	envCmd.Env = []string{"REDIR=2>&1"}

	tests := []struct {
		name       string
		cmd        invoke.Command
		wantStdout string
		wantStderr string
	}{
		{
			name:       "single-quoted duplication stays a word",
			cmd:        invoke.Shell(`echo '2>&1'`),
			wantStdout: "2>&1\n",
		},
		{
			name:       "double-quoted duplication stays a word",
			cmd:        invoke.Shell(`echo "2>&1"`),
			wantStdout: "2>&1\n",
		},
		{
			name:       "single-quoted null redirect stays a word",
			cmd:        invoke.Shell(`echo '>/dev/null'`),
			wantStdout: ">/dev/null\n",
		},
		{
			name:       "an expanded value is data wherever it lands",
			cmd:        envCmd,
			wantStdout: "2>&1\n",
		},
		{
			name:       "a substituted value is data wherever it lands",
			cmd:        invoke.Shell(`echo $(printf '2>&1')`),
			wantStdout: "2>&1\n",
		},
		{
			name:       "a bare null redirect still rewires",
			cmd:        invoke.Shell(`echo hi >/dev/null`),
			wantStdout: "",
		},
		{
			name:       "a bare duplication still rewires",
			cmd:        invoke.Shell(`echo hi 1>&2`),
			wantStderr: "hi\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := fake.New()

			t.Cleanup(func() { _ = env.Close() })

			var stdout, stderr bytes.Buffer

			proc, err := env.Start(t.Context(), tt.cmd, invoke.IO{Stdout: &stdout, Stderr: &stderr})
			require.NoError(t, err, "every script here is within the subset")

			res, waitErr := proc.Wait()
			require.NoError(t, waitErr, "want exit 0")
			require.Equal(t, 0, res.ExitCode, "want exit 0")

			assert.Equal(t, tt.wantStdout, stdout.String(),
				"stdout must match what a real shell produces")
			assert.Equal(t, tt.wantStderr, stderr.String(),
				"stderr must match what a real shell produces")
		})
	}
}

// TestUnsupportedSyntaxNestedInQuotesIsRefusedLoudly covers the script
// the check at Start cannot see: quoted, and so opaque until it runs.
func TestUnsupportedSyntaxNestedInQuotesIsRefusedLoudly(t *testing.T) {
	t.Parallel()

	env := fake.New()

	t.Cleanup(func() { _ = env.Close() })

	var stdout, stderr bytes.Buffer

	proc, err := env.Start(t.Context(),
		invoke.Shell(`sh -c 'echo hi | tr h H'`), invoke.IO{Stdout: &stdout, Stderr: &stderr})
	require.NoError(t, err, "the outer script is within the subset")

	result, waitErr := proc.Wait()
	require.Error(t, waitErr, "a nested script the shell cannot run must not report success")

	assert.NotEqual(t, 0, result.ExitCode)
	assert.Contains(t, stderr.String(), "not simulated",
		"the failure must say what the shell could not run")
	assert.Empty(t, stdout.String(), "nothing must be produced by a script that did not run")
}
