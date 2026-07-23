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
