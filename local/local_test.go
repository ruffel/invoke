package local_test

import (
	"errors"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/local"
)

// newEnv constructs an Environment and ties its lifetime to the test.
func newEnv(t *testing.T) *local.Environment {
	t.Helper()

	env, err := local.New()
	if err != nil {
		t.Fatalf("local.New() = %v", err)
	}

	t.Cleanup(func() { _ = env.Close() })

	return env
}

func TestNew(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	if got := env.OS(); string(got) != runtime.GOOS {
		t.Errorf("OS() = %q, want %q", got, runtime.GOOS)
	}

	caps := env.Capabilities()
	if caps.TTY {
		t.Error("Capabilities().TTY = true; local does not implement PTY allocation this cycle")
	}

	if !caps.Signals || !caps.SymlinkPreserve {
		t.Errorf("Capabilities() = %+v, want Signals and SymlinkPreserve declared", caps)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	if err := env.Close(); err != nil {
		t.Fatalf("first Close() = %v", err)
	}

	if err := env.Close(); err != nil {
		t.Fatalf("second Close() = %v", err)
	}
}

func TestClosedEnvironmentRefusesEverything(t *testing.T) {
	t.Parallel()

	env := newEnv(t)
	_ = env.Close()

	ctx := t.Context()

	if _, err := env.Start(ctx, invoke.New("true"), invoke.IO{}); !errors.Is(err, invoke.ErrClosed) {
		t.Errorf("Start after Close = %v, want ErrClosed", err)
	}

	if _, err := env.LookPath(ctx, "sh"); !errors.Is(err, invoke.ErrClosed) {
		t.Errorf("LookPath after Close = %v, want ErrClosed", err)
	}

	if err := env.Upload(ctx, "a", "b"); !errors.Is(err, invoke.ErrClosed) {
		t.Errorf("Upload after Close = %v, want ErrClosed", err)
	}

	if err := env.Download(ctx, "a", "b"); !errors.Is(err, invoke.ErrClosed) {
		t.Errorf("Download after Close = %v, want ErrClosed", err)
	}
}

func TestLookPath(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	path, err := env.LookPath(t.Context(), "sh")
	if err != nil {
		t.Fatalf("LookPath(sh) = %v", err)
	}

	if !filepath.IsAbs(path) {
		t.Errorf("LookPath(sh) = %q, want an absolute path", path)
	}

	_, err = env.LookPath(t.Context(), "definitely-not-a-real-binary-abc123")
	if !errors.Is(err, invoke.ErrNotFound) {
		t.Errorf("LookPath(missing) = %v, want ErrNotFound", err)
	}
}
