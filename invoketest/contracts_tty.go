package invoketest

import (
	"errors"

	"github.com/ruffel/invoke"
)

func ttyContracts() []TestCase {
	return []TestCase{
		ttyAllocatesTerminal(),
		ttyUnsupportedErrors(),
	}
}

func ttyAllocatesTerminal() TestCase {
	return TestCase{
		Category:    CategoryTTY,
		Name:        "allocates-terminal",
		Description: "With the TTY capability declared, IO.TTY gives the process a real terminal on stdin",
		Gate: func(caps invoke.Capabilities) (bool, string) {
			return caps.TTY, "target does not declare TTY allocation; tty/unsupported-errors covers it"
		},
		Run: func(t T, env invoke.Environment) {
			outcome, _, _ := runCapture(t, env, invoke.Shell("test -t 0"))
			_ = outcome // Without a TTY request, test -t 0 exiting either way is fine.

			proc := startCommand(t.Context(), t, env, invoke.Shell("test -t 0"),
				invoke.IO{TTY: &invoke.TTY{}})

			result := waitOrTimeout(t, proc)
			if result.err != nil {
				t.Errorf("test -t 0 under a requested TTY = %v; the process did not get a terminal", result.err)
			}
		},
	}
}

func ttyUnsupportedErrors() TestCase {
	return TestCase{
		Category:    CategoryTTY,
		Name:        "unsupported-errors",
		Description: "Without the TTY capability, requesting one fails wrapping ErrNotSupported instead of being ignored",
		Gate: func(caps invoke.Capabilities) (bool, string) {
			return !caps.TTY, "target declares TTY allocation; tty/allocates-terminal covers it"
		},
		Run: func(t T, env invoke.Environment) {
			proc, err := env.Start(t.Context(), invoke.New("true"), invoke.IO{TTY: &invoke.TTY{}})
			if err == nil {
				if proc != nil {
					_, _ = proc.Wait()
				}

				failf(t, "requesting a TTY on a target without the capability succeeded silently")
			}

			if !errors.Is(err, invoke.ErrNotSupported) {
				t.Errorf("TTY request = %v, want an error wrapping ErrNotSupported", err)
			}
		},
	}
}
