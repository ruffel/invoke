package invoketest

import (
	"bytes"
	"path"
	"strings"
	"time"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// largeOutputBytes exceeds the classic 64 KiB pipe buffer several times
// over, so a provider that drains output only after exit deadlocks (and
// fails by timeout) instead of passing by luck. largeStderrBytes does the
// same for the stderr stream at a quarter of the size.
const (
	largeOutputBytes = 256 * 1024
	largeStderrBytes = 64 * 1024
)

func coreContracts() []TestCase {
	return []TestCase{
		coreCapturesStdout(),
		coreStreamsStaySeparate(),
		coreNilStdinIsEOF(),
		coreStdinIsDelivered(),
		coreLargeStdinIsDelivered(),
		coreLargeOutputIsComplete(),
		coreEnvOverlaysBase(),
		coreEnvOverrideWins(),
		coreEnvPathResolvesCommand(),
		coreWorkdirIsHonored(),
		coreArgsAreLiteral(),
		coreExitCodeIsReported(),
		coreExitCodePastSignalBoundary(),
		coreDurationIsMeasured(),
		coreOSMatchesTarget(),
	}
}

func coreCapturesStdout() TestCase {
	return TestCase{
		Category:    CategoryCore,
		Name:        "captures-stdout",
		Description: "Standard output reaches the caller's writer exactly",
		Run: func(t T, env invoke.Environment) {
			outcome, stdout, _ := runCapture(t, env, invoke.New("echo", "hello", "world"))
			require.NoError(t, outcome.err, "echo failed")

			assert.Equal(t, 0, outcome.result.ExitCode)
			assert.Equal(t, "hello world\n", stdout)
		},
	}
}

func coreStreamsStaySeparate() TestCase {
	return TestCase{
		Category:    CategoryCore,
		Name:        "streams-stay-separate",
		Description: "Stdout and stderr reach their own writers, unmixed, without a TTY",
		Run: func(t T, env invoke.Environment) {
			outcome, stdout, stderr := runCapture(t, env, invoke.Shell("echo out; echo err 1>&2"))
			require.NoError(t, outcome.err, "shell failed")

			assert.Equal(t, "out\n", stdout)
			assert.Equal(t, "err\n", stderr)
		},
	}
}

func coreNilStdinIsEOF() TestCase {
	return TestCase{
		Category:    CategoryCore,
		Name:        "nil-stdin-is-eof",
		Description: "A nil Stdin reads immediate EOF; the process neither hangs nor inherits input",
		Run: func(t T, env invoke.Environment) {
			outcome, stdout, _ := runCapture(t, env, invoke.New("cat"))
			require.NoError(t, outcome.err, "cat with nil stdin failed")

			assert.Empty(t, stdout, "cat with nil stdin produced output; stdin must be empty, not inherited")
		},
	}
}

func coreStdinIsDelivered() TestCase {
	return TestCase{
		Category:    CategoryCore,
		Name:        "stdin-is-delivered",
		Description: "Bytes from the caller's Stdin reach the process intact",
		Run: func(t T, env invoke.Environment) {
			var stdout bytes.Buffer

			proc := startCommand(t.Context(), t, env, invoke.New("cat"), invoke.IO{
				Stdin:  strings.NewReader("piped through"),
				Stdout: &stdout,
			})

			outcome := waitOrTimeout(t, proc)
			require.NoError(t, outcome.err, "cat failed")

			assert.Equal(t, "piped through", stdout.String())
		},
	}
}

func coreLargeStdinIsDelivered() TestCase {
	return TestCase{
		Category:    CategoryCore,
		Name:        "large-stdin-is-delivered",
		Description: "Stdin far beyond pipe-buffer size reaches the process intact, without deadlock",
		Run: func(t T, env invoke.Environment) {
			const chunk = "abcdefgh"

			payload := strings.Repeat(chunk, largeOutputBytes/len(chunk))

			var stdout bytes.Buffer

			proc := startCommand(t.Context(), t, env, invoke.New("cat"), invoke.IO{
				Stdin:  strings.NewReader(payload),
				Stdout: &stdout,
			})

			outcome := waitOrTimeout(t, proc)
			require.NoError(t, outcome.err, "cat failed")

			assert.Len(t, stdout.String(), len(payload), "cat echoed a different number of bytes")
			assert.Equal(t, payload, stdout.String(), "large stdin was corrupted in transit")
		},
	}
}

func coreEnvOverrideWins() TestCase {
	return TestCase{
		Category:    CategoryCore,
		Name:        "env-override-wins",
		Description: "Command.Env overrides an existing base variable, and the last of duplicate keys wins",
		Run: func(t T, env invoke.Environment) {
			// HOME is in every provider's base environment; the overlay
			// must win over it. The duplicate INVOKE_DUP entries pin
			// os/exec's last-wins semantics.
			cmd := invoke.Shell(`printf '%s|%s' "$HOME" "$INVOKE_DUP"`)
			cmd.Env = []string{"HOME=/overridden", "INVOKE_DUP=first", "INVOKE_DUP=second"}

			stdout := runSucceeds(t, env, cmd)

			home, dup, _ := strings.Cut(stdout, "|")
			assert.Equal(t, "/overridden", home, "$HOME: the overlay value must win over the base")
			assert.Equal(t, "second", dup, "$INVOKE_DUP: the last of duplicate keys must win")
		},
	}
}

// coreEnvPathResolvesCommand pins what a PATH in Command.Env means: it is
// the PATH the command is found through, as well as the one it runs
// with.
//
// The alternative reading, where the target's own PATH finds the command
// and the caller's only reaches it afterwards, makes Command.Env say one
// thing and do another. It also makes providers disagree, since some
// resolve the name inside the environment the caller built and some
// before it exists.
func coreEnvPathResolvesCommand() TestCase {
	return TestCase{
		Category:    CategoryCore,
		Name:        "env-path-resolves-command",
		Description: "A PATH in Command.Env is the one a bare command name is resolved against",
		Run: func(t T, env invoke.Environment) {
			found, err := env.LookPath(t.Context(), "true")
			require.NoError(t, err, "the target needs true for this contract")

			nowhere := "/invoke-no-such-dir-" + token(t)

			// The caller's PATH lists where true lives, after a directory
			// that does not exist: the command is found through it.
			listed := invoke.New("true")
			listed.Env = []string{"PATH=" + nowhere + ":" + path.Dir(found)}

			outcome, _, _ := runCapture(t, env, listed)
			require.NoError(t, outcome.err, "true is on the caller's PATH, so it must run")

			// The caller's PATH lists nowhere true lives: the command is
			// not found, whatever the target's own PATH would have said.
			unlisted := invoke.New("true")
			unlisted.Env = []string{"PATH=" + nowhere}

			proc, err := env.Start(t.Context(), unlisted, invoke.IO{})
			if err == nil {
				outcome := waitOrTimeout(t, proc)
				_ = proc.Close()

				require.Failf(t, "the target's own PATH found the command",
					"true is not on the caller's PATH %q, yet it ran (%v)", nowhere, outcome.err)
			}

			assert.ErrorIs(t, err, invoke.ErrNotFound, "a command missing from the caller's PATH")
		},
	}
}

func coreArgsAreLiteral() TestCase {
	return TestCase{
		Category:    CategoryCore,
		Name:        "args-are-literal",
		Description: "Arguments reach the process verbatim, with no shell interpretation of spaces, quotes, or metacharacters",
		Run: func(t T, env invoke.Environment) {
			// Each argument is delivered to printf %s and must round-trip
			// exactly. A provider that shell-joins argv (the classic
			// remote-command-line hazard) corrupts these.
			args := []string{
				"a b c",           // internal spaces
				"has'single",      // single quote
				`has"double`,      // double quote
				"semi;colon",      // command separator
				"dollar$VAR",      // no expansion must occur
				"star*glob?",      // no globbing must occur
				"back`tick`",      // no substitution must occur
				"",                // empty argument must survive
				"trailing-space ", // trailing whitespace
			}

			printfArgs := append([]string{"[%s]\n"}, args...)

			outcome, stdout, _ := runCapture(t, env, invoke.New("printf", printfArgs...))
			require.NoError(t, outcome.err, "printf failed")

			var want strings.Builder
			for _, arg := range args {
				want.WriteString("[" + arg + "]\n")
			}

			assert.Equal(t, want.String(), stdout, "argv round-trip mismatch")
		},
	}
}

func coreExitCodePastSignalBoundary() TestCase {
	return TestCase{
		Category:    CategoryCore,
		Name:        "exit-code-past-signal-boundary",
		Description: "A plain exit with a status of 128 or more stays an exit code, not a signal death",
		Run: func(t T, env invoke.Environment) {
			// 137 is 128+9; a provider that reads any status >= 128 as a
			// signal (the shell's own convention, but wrong for a plain
			// exit) would report a SIGKILL death here.
			const wantCode = 137

			outcome, _, _ := runCapture(t, env, invoke.Shell("exit 137"))

			exitErr := requireExitError(t, outcome.err)
			assert.Equal(t, wantCode, exitErr.Code)
			assert.Empty(t, exitErr.Signal, "a plain exit 137 is not a signal death")
			assert.Equal(t, wantCode, outcome.result.ExitCode)
		},
	}
}

func coreOSMatchesTarget() TestCase {
	return TestCase{
		Category:    CategoryCore,
		Name:        "os-matches-target",
		Description: "OS() agrees with the target's own uname, and is never OSUnknown for a working target",
		Run: func(t T, env invoke.Environment) {
			require.NotEqual(t, invoke.OSUnknown, env.OS(), "OS() must not be OSUnknown for a working target")

			outcome, stdout, _ := runCapture(t, env, invoke.New("uname", "-s"))
			if outcome.err != nil {
				t.Skipf("target has no usable uname: %v", outcome.err)
			}

			want, ok := osFromUname(strings.TrimSpace(stdout))
			if !ok {
				t.Skipf("uname reported %q, outside the declared OS set", strings.TrimSpace(stdout))
			}

			assert.Equal(t, want, env.OS(), "OS() must agree with the target's own uname")
		},
	}
}

// osFromUname maps a uname -s string onto a declared TargetOS.
func osFromUname(name string) (invoke.TargetOS, bool) {
	switch name {
	case "Linux":
		return invoke.OSLinux, true
	case "Darwin":
		return invoke.OSDarwin, true
	default:
		return invoke.OSUnknown, false
	}
}

func coreLargeOutputIsComplete() TestCase {
	return TestCase{
		Category:    CategoryCore,
		Name:        "large-output-is-complete",
		Description: "Output far beyond pipe-buffer size arrives completely on both streams without deadlock",
		Run: func(t T, env invoke.Environment) {
			script := "dd if=/dev/zero bs=1024 count=256 2>/dev/null; dd if=/dev/zero bs=1024 count=64 1>&2 2>/dev/null"

			outcome, stdout, stderr := runCapture(t, env, invoke.Shell(script))
			require.NoError(t, outcome.err, "large-output command failed")

			assert.Len(t, stdout, largeOutputBytes, "stdout carried the wrong number of bytes")
			assert.Len(t, stderr, largeStderrBytes, "stderr carried the wrong number of bytes")
		},
	}
}

func coreEnvOverlaysBase() TestCase {
	return TestCase{
		Category:    CategoryCore,
		Name:        "env-overlays-base",
		Description: "Command.Env applies over the target's base environment without replacing it",
		Run: func(t T, env invoke.Environment) {
			cmd := invoke.Shell(`printf '%s|%s' "$INVOKE_CONTRACT_VALUE" "$PATH"`)
			cmd.Env = []string{"INVOKE_CONTRACT_VALUE=overlaid"}

			stdout := runSucceeds(t, env, cmd)

			value, path, _ := strings.Cut(stdout, "|")
			assert.Equal(t, "overlaid", value)
			assert.NotEmpty(t, path, "PATH is empty: the overlay must not replace the base environment")
		},
	}
}

func coreWorkdirIsHonored() TestCase {
	return TestCase{
		Category:    CategoryCore,
		Name:        "workdir-is-honored",
		Description: "Command.Dir sets the process working directory on the target",
		Run: func(t T, env invoke.Environment) {
			dir := "/tmp/invoke-wd-" + token(t)

			runSucceeds(t, env, invoke.Shell("mkdir -p "+shellQuote(dir)))
			defer cleanupTargetPath(t, env, dir)

			cmd := invoke.Shell("pwd")
			cmd.Dir = dir

			got := strings.TrimSpace(runSucceeds(t, env, cmd))

			// Resolve through the target's own shell so paths like
			// macOS's symlinked /tmp compare equal.
			resolved := strings.TrimSpace(runSucceeds(t, env,
				invoke.Shell("cd "+shellQuote(dir)+" && pwd -P")))

			assert.Contains(t, []string{dir, resolved}, got,
				"pwd must report the requested working directory, or its resolved form")
		},
	}
}

func coreExitCodeIsReported() TestCase {
	return TestCase{
		Category:    CategoryCore,
		Name:        "exit-code-is-reported",
		Description: "A non-zero exit surfaces as an ExitError agreeing with the Result",
		Run: func(t T, env invoke.Environment) {
			const wantCode = 19

			outcome, _, _ := runCapture(t, env, invoke.Shell("exit 19"))

			exitErr := requireExitError(t, outcome.err)
			assert.Equal(t, wantCode, exitErr.Code)
			assert.Empty(t, exitErr.Signal, "a plain exit is not a signal death")
			assert.Equal(t, wantCode, outcome.result.ExitCode)
		},
	}
}

// minMeasuredDuration is the floor for the measured-duration contract:
// far below the 200ms the command actually sleeps, far above zero, so it
// is immune to scheduler noise while still catching an unset field.
const minMeasuredDuration = 50 * time.Millisecond

func coreDurationIsMeasured() TestCase {
	return TestCase{
		Category:    CategoryCore,
		Name:        "duration-is-measured",
		Description: "Result.Duration reflects real elapsed time, not a zero value",
		Run: func(t T, env invoke.Environment) {
			outcome, _, _ := runCapture(t, env, invoke.Shell("sleep 0.2"))
			require.NoError(t, outcome.err, "sleep failed")

			assert.GreaterOrEqual(t, outcome.result.Duration, minMeasuredDuration,
				"Duration for a 200ms sleep; the field is not being measured")
		},
	}
}
