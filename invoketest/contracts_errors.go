package invoketest

import (
	"errors"

	"github.com/ruffel/invoke"
)

func errorsContracts() []TestCase {
	return []TestCase{
		errorsMissingBinaryNotFound(),
		errorsBadWorkdirClassified(),
		errorsLookPathClassifies(),
		errorsClosedEnvRefusesAll(),
	}
}

func errorsMissingBinaryNotFound() TestCase {
	return TestCase{
		Category:    CategoryErrors,
		Name:        "missing-binary-not-found",
		Description: "A binary the target cannot resolve fails wrapping ErrNotFound",
		Run: func(t T, env invoke.Environment) {
			proc, err := env.Start(t.Context(),
				invoke.New("invoke-definitely-missing-"+token(t)), invoke.IO{})
			if err == nil {
				if proc != nil {
					_ = proc.Close()
				}

				failf(t, "starting a nonexistent binary succeeded")
			}

			if !errors.Is(err, invoke.ErrNotFound) {
				t.Errorf("error = %v, want an error wrapping ErrNotFound", err)
			}
		},
	}
}

func errorsBadWorkdirClassified() TestCase {
	return TestCase{
		Category:    CategoryErrors,
		Name:        "bad-workdir-classified",
		Description: "A nonexistent working directory fails wrapping ErrInvalidWorkdir, not as a command outcome",
		Run: func(t T, env invoke.Environment) {
			cmd := invoke.New("true")
			cmd.Dir = "/tmp/invoke-no-such-dir-" + token(t)

			proc, err := env.Start(t.Context(), cmd, invoke.IO{})
			if err == nil {
				if proc != nil {
					_, _ = proc.Wait()
				}

				failf(t, "starting in a nonexistent workdir reported no error")
			}

			if !errors.Is(err, invoke.ErrInvalidWorkdir) {
				t.Errorf("error = %v, want an error wrapping ErrInvalidWorkdir", err)
			}

			requireNotExitError(t, err, "a workdir setup failure")
		},
	}
}

func errorsLookPathClassifies() TestCase {
	return TestCase{
		Category:    CategoryErrors,
		Name:        "lookpath-classifies",
		Description: "LookPath resolves real names and wraps ErrNotFound for unresolvable ones",
		Run: func(t T, env invoke.Environment) {
			path, err := env.LookPath(t.Context(), "sh")
			if err != nil || path == "" {
				failf(t, "LookPath(sh) = (%q, %v), want a resolved path", path, err)
			}

			_, err = env.LookPath(t.Context(), "invoke-definitely-missing-"+token(t))
			if !errors.Is(err, invoke.ErrNotFound) {
				t.Errorf("LookPath(missing) = %v, want an error wrapping ErrNotFound", err)
			}
		},
	}
}

func errorsClosedEnvRefusesAll() TestCase {
	return TestCase{
		Category:    CategoryErrors,
		Name:        "closed-env-refuses-all",
		Description: "After Close, every method fails wrapping ErrClosed",
		Run: func(t T, env invoke.Environment) {
			if err := env.Close(); err != nil {
				failf(t, "Close = %v", err)
			}

			ctx := t.Context()

			if _, err := env.Start(ctx, invoke.New("true"), invoke.IO{}); !errors.Is(err, invoke.ErrClosed) {
				t.Errorf("Start after Close = %v, want an error wrapping ErrClosed", err)
			}

			if _, err := env.LookPath(ctx, "sh"); !errors.Is(err, invoke.ErrClosed) {
				t.Errorf("LookPath after Close = %v, want an error wrapping ErrClosed", err)
			}

			if err := env.Upload(ctx, "src", "dst"); !errors.Is(err, invoke.ErrClosed) {
				t.Errorf("Upload after Close = %v, want an error wrapping ErrClosed", err)
			}

			if err := env.Download(ctx, "src", "dst"); !errors.Is(err, invoke.ErrClosed) {
				t.Errorf("Download after Close = %v, want an error wrapping ErrClosed", err)
			}
		},
	}
}
