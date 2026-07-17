package fake_test

import (
	"bytes"
	"context"
	"errors"
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
	if err != nil {
		t.Fatalf("Start = %v", err)
	}

	res, err := proc.Wait()
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("Wait = (%+v, %v), want success", res, err)
	}

	if got := stdout.String(); got != "deployed api with config" {
		t.Errorf("stdout = %q", got)
	}

	if got := stderr.String(); got != "warning: simulated\n" {
		t.Errorf("stderr = %q", got)
	}

	calls := env.Calls()
	if len(calls) != 1 || calls[0].Path != "deploy" || calls[0].Args[1] != "--fast" {
		t.Errorf("Calls() = %+v, want the deploy invocation recorded", calls)
	}
}

func TestHandlerNonZeroExitIsExitError(t *testing.T) {
	t.Parallel()

	env := fake.New()

	t.Cleanup(func() { _ = env.Close() })

	env.Handle("flaky", func(_ context.Context, _ invoke.Command, _ *fake.Session) int {
		return 3
	})

	proc, err := env.Start(t.Context(), invoke.New("flaky"), invoke.IO{})
	if err != nil {
		t.Fatalf("Start = %v", err)
	}

	res, waitErr := proc.Wait()

	var exitErr *invoke.ExitError
	if !errors.As(waitErr, &exitErr) || exitErr.Code != 3 || res.ExitCode != 3 {
		t.Errorf("Wait = (%+v, %v), want ExitError code 3", res, waitErr)
	}
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
	if err != nil {
		t.Fatalf("Start = %v", err)
	}

	cancel()

	_, waitErr := proc.Wait()
	if !errors.Is(waitErr, context.Canceled) {
		t.Errorf("Wait = %v, want context.Canceled", waitErr)
	}
}

func TestWithEnvSeedsBaseEnvironment(t *testing.T) {
	t.Parallel()

	env := fake.New(fake.WithEnv("REGION=eu-west-1"))

	t.Cleanup(func() { _ = env.Close() })

	var stdout bytes.Buffer

	proc, err := env.Start(t.Context(), invoke.Shell(`printf '%s' "$REGION"`), invoke.IO{Stdout: &stdout})
	if err != nil {
		t.Fatalf("Start = %v", err)
	}

	if _, err := proc.Wait(); err != nil {
		t.Fatalf("Wait = %v", err)
	}

	if got := stdout.String(); got != "eu-west-1" {
		t.Errorf("$REGION = %q, want %q", got, "eu-west-1")
	}
}

func TestFSViewExposesTargetState(t *testing.T) {
	t.Parallel()

	env := fake.New()

	t.Cleanup(func() { _ = env.Close() })

	srcDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(srcDir, "config.json"), []byte(`{"ok":true}`), 0o644); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	if err := os.Symlink("config.json", filepath.Join(srcDir, "current.json")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if err := env.Upload(t.Context(), srcDir, "/etc/app"); err != nil {
		t.Fatalf("Upload = %v", err)
	}

	view := env.FS()

	// The adapter must satisfy the stdlib's own conformance test.
	if err := fstest.TestFS(view, "etc/app/config.json", "etc/app/current.json"); err != nil {
		t.Fatalf("fstest.TestFS: %v", err)
	}

	content, err := fs.ReadFile(view, "etc/app/config.json")
	if err != nil || string(content) != `{"ok":true}` {
		t.Errorf("ReadFile = (%q, %v)", content, err)
	}

	linkFS, ok := view.(fs.ReadLinkFS)
	if !ok {
		t.Fatal("FS view does not implement fs.ReadLinkFS")
	}

	target, err := linkFS.ReadLink("etc/app/current.json")
	if err != nil || target != "config.json" {
		t.Errorf("ReadLink = (%q, %v), want config.json", target, err)
	}
}

func TestUnknownCommandIsNotFound(t *testing.T) {
	t.Parallel()

	env := fake.New()

	t.Cleanup(func() { _ = env.Close() })

	_, err := env.Start(t.Context(), invoke.New("unscripted-command"), invoke.IO{})
	if !errors.Is(err, invoke.ErrNotFound) {
		t.Errorf("Start(unscripted) = %v, want ErrNotFound", err)
	}
}
