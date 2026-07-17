package invoketest

import (
	"bytes"
	"strings"
	"time"

	"github.com/ruffel/invoke"
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
			if outcome.err != nil {
				failf(t, "echo failed: %v", outcome.err)
			}

			if outcome.result.ExitCode != 0 {
				t.Errorf("ExitCode = %d, want 0", outcome.result.ExitCode)
			}

			if stdout != "hello world\n" {
				t.Errorf("stdout = %q, want %q", stdout, "hello world\n")
			}
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
			if outcome.err != nil {
				failf(t, "shell failed: %v", outcome.err)
			}

			if stdout != "out\n" {
				t.Errorf("stdout = %q, want %q", stdout, "out\n")
			}

			if stderr != "err\n" {
				t.Errorf("stderr = %q, want %q", stderr, "err\n")
			}
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
			if outcome.err != nil {
				failf(t, "cat with nil stdin failed: %v", outcome.err)
			}

			if stdout != "" {
				t.Errorf("cat with nil stdin produced %q; stdin must be empty, not inherited", stdout)
			}
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
			if outcome.err != nil {
				failf(t, "cat failed: %v", outcome.err)
			}

			if got := stdout.String(); got != "piped through" {
				t.Errorf("stdout = %q, want %q", got, "piped through")
			}
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
			if outcome.err != nil {
				failf(t, "cat failed: %v", outcome.err)
			}

			if got := stdout.Len(); got != len(payload) {
				t.Errorf("cat echoed %d bytes, want %d", got, len(payload))
			}

			if stdout.String() != payload {
				t.Errorf("large stdin was corrupted in transit")
			}
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
			if home != "/overridden" {
				t.Errorf("$HOME = %q, want the overlay value /overridden to win over the base", home)
			}

			if dup != "second" {
				t.Errorf("$INVOKE_DUP = %q, want the last duplicate (second) to win", dup)
			}
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
			if outcome.err != nil {
				failf(t, "printf failed: %v", outcome.err)
			}

			var want strings.Builder
			for _, arg := range args {
				want.WriteString("[" + arg + "]\n")
			}

			if stdout != want.String() {
				t.Errorf("argv round-trip mismatch:\n got %q\nwant %q", stdout, want.String())
			}
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
			if exitErr.Code != wantCode {
				t.Errorf("ExitError.Code = %d, want %d", exitErr.Code, wantCode)
			}

			if exitErr.Signal != "" {
				t.Errorf("ExitError.Signal = %q for a plain exit 137, want empty", exitErr.Signal)
			}

			if outcome.result.ExitCode != wantCode {
				t.Errorf("Result.ExitCode = %d, want %d", outcome.result.ExitCode, wantCode)
			}
		},
	}
}

func coreOSMatchesTarget() TestCase {
	return TestCase{
		Category:    CategoryCore,
		Name:        "os-matches-target",
		Description: "OS() agrees with the target's own uname, and is never OSUnknown for a working target",
		Run: func(t T, env invoke.Environment) {
			if env.OS() == invoke.OSUnknown {
				failf(t, "OS() = OSUnknown for a working target")
			}

			outcome, stdout, _ := runCapture(t, env, invoke.New("uname", "-s"))
			if outcome.err != nil {
				t.Skipf("target has no usable uname: %v", outcome.err)
			}

			want, ok := osFromUname(strings.TrimSpace(stdout))
			if !ok {
				t.Skipf("uname reported %q, outside the declared OS set", strings.TrimSpace(stdout))
			}

			if env.OS() != want {
				t.Errorf("OS() = %q, but the target's uname says %q", env.OS(), want)
			}
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
			if outcome.err != nil {
				failf(t, "large-output command failed: %v", outcome.err)
			}

			if len(stdout) != largeOutputBytes {
				t.Errorf("stdout carried %d bytes, want %d", len(stdout), largeOutputBytes)
			}

			if len(stderr) != largeStderrBytes {
				t.Errorf("stderr carried %d bytes, want %d", len(stderr), largeStderrBytes)
			}
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
			if value != "overlaid" {
				t.Errorf("overlay variable = %q, want %q", value, "overlaid")
			}

			if path == "" {
				t.Errorf("PATH is empty: the overlay must not replace the base environment")
			}
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

			if got != dir && got != resolved {
				t.Errorf("pwd = %q, want %q (or resolved %q)", got, dir, resolved)
			}
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
			if exitErr.Code != wantCode {
				t.Errorf("ExitError.Code = %d, want %d", exitErr.Code, wantCode)
			}

			if exitErr.Signal != "" {
				t.Errorf("ExitError.Signal = %q for a plain exit, want empty", exitErr.Signal)
			}

			if outcome.result.ExitCode != wantCode {
				t.Errorf("Result.ExitCode = %d, want %d", outcome.result.ExitCode, wantCode)
			}
		},
	}
}

func coreDurationIsMeasured() TestCase {
	return TestCase{
		Category:    CategoryCore,
		Name:        "duration-is-measured",
		Description: "Result.Duration reflects real elapsed time, not a zero value",
		Run: func(t T, env invoke.Environment) {
			outcome, _, _ := runCapture(t, env, invoke.Shell("sleep 0.2"))
			if outcome.err != nil {
				failf(t, "sleep failed: %v", outcome.err)
			}

			// Far below the real 200ms, far above zero: immune to
			// scheduler noise while catching an unset field.
			if outcome.result.Duration < 50*time.Millisecond {
				t.Errorf("Duration = %v for a 200ms sleep; the field is not being measured", outcome.result.Duration)
			}
		},
	}
}
