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
		coreLargeOutputIsComplete(),
		coreEnvOverlaysBase(),
		coreWorkdirIsHonored(),
		coreExitCodeIsReported(),
		coreDurationIsMeasured(),
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
