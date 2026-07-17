package local_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ruffel/invoke"
)

const (
	// promptly bounds operations the contracts require to return in
	// bounded time (unblocking waits, delivering kills). It comfortably
	// exceeds the provider's internal wait grace period.
	promptly = 4 * time.Second

	// wantExitCode is an arbitrary distinctive exit status.
	wantExitCode = 19
)

// run starts cmd, waits for it, and returns the outcome plus captured
// stdout and stderr.
func run(t *testing.T, cmd invoke.Command) (invoke.Result, string, string, error) {
	t.Helper()

	env := newEnv(t)

	var stdout, stderr bytes.Buffer

	proc, err := env.Start(t.Context(), cmd, invoke.IO{Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		t.Fatalf("Start(%v) = %v", cmd, err)
	}

	res, waitErr := proc.Wait()

	return res, stdout.String(), stderr.String(), waitErr
}

// waitInBackground calls proc.Wait on a goroutine and returns a channel
// carrying its outcome.
func waitInBackground(proc invoke.Process) <-chan error {
	done := make(chan error, 1)

	go func() {
		_, err := proc.Wait()
		done <- err
	}()

	return done
}

func TestRunCapturesOutput(t *testing.T) {
	t.Parallel()

	res, stdout, stderr, err := run(t, invoke.New("echo", "hello", "world"))
	if err != nil {
		t.Fatalf("Wait() = %v", err)
	}

	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}

	if stdout != "hello world\n" {
		t.Errorf("stdout = %q, want %q", stdout, "hello world\n")
	}

	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestStreamsStaySeparate(t *testing.T) {
	t.Parallel()

	_, stdout, stderr, err := run(t, invoke.Shell("echo out; echo err 1>&2"))
	if err != nil {
		t.Fatalf("Wait() = %v", err)
	}

	if stdout != "out\n" || stderr != "err\n" {
		t.Errorf("stdout=%q stderr=%q, want separated streams", stdout, stderr)
	}
}

func TestNonZeroExitIsExitError(t *testing.T) {
	t.Parallel()

	res, _, _, err := run(t, invoke.Shell("exit 19"))

	var exitErr *invoke.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Wait() = %v, want *ExitError", err)
	}

	if exitErr.Code != wantExitCode || exitErr.Signal != "" {
		t.Errorf("ExitError = {Code:%d Signal:%q}, want {Code:%d}", exitErr.Code, exitErr.Signal, wantExitCode)
	}

	if res.ExitCode != wantExitCode {
		t.Errorf("Result.ExitCode = %d, want %d", res.ExitCode, wantExitCode)
	}

	if res.Duration <= 0 {
		t.Errorf("Result.Duration = %v, want > 0", res.Duration)
	}
}

func TestNilStdinIsImmediateEOF(t *testing.T) {
	t.Parallel()

	// cat with no stdin wiring must see EOF and exit, not inherit the
	// test process's stdin or hang.
	res, stdout, _, err := run(t, invoke.New("cat"))
	if err != nil {
		t.Fatalf("Wait() = %v", err)
	}

	if res.ExitCode != 0 || stdout != "" {
		t.Errorf("cat with nil stdin: exit=%d stdout=%q, want 0 and empty", res.ExitCode, stdout)
	}
}

func TestStdinIsDelivered(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	var stdout bytes.Buffer

	proc, err := env.Start(t.Context(), invoke.New("cat"), invoke.IO{
		Stdin:  strings.NewReader("piped through"),
		Stdout: &stdout,
	})
	if err != nil {
		t.Fatalf("Start = %v", err)
	}

	if _, err := proc.Wait(); err != nil {
		t.Fatalf("Wait() = %v", err)
	}

	if got := stdout.String(); got != "piped through" {
		t.Errorf("stdout = %q, want %q", got, "piped through")
	}
}

func TestLargeOutputDoesNotDeadlock(t *testing.T) {
	t.Parallel()

	const wantBytes = 256 * 1024

	_, stdout, _, err := run(t, invoke.Shell("dd if=/dev/zero bs=1024 count=256 2>/dev/null"))
	if err != nil {
		t.Fatalf("Wait() = %v", err)
	}

	if len(stdout) != wantBytes {
		t.Errorf("captured %d bytes, want %d", len(stdout), wantBytes)
	}
}

func TestEnvOverlaysBaseEnvironment(t *testing.T) {
	t.Parallel()

	cmd := invoke.Shell(`printf '%s|%s' "$INVOKE_TEST_VALUE" "$PATH"`)
	cmd.Env = []string{"INVOKE_TEST_VALUE=overlaid"}

	_, stdout, _, err := run(t, cmd)
	if err != nil {
		t.Fatalf("Wait() = %v", err)
	}

	value, path, _ := strings.Cut(stdout, "|")
	if value != "overlaid" {
		t.Errorf("overlay variable = %q, want %q", value, "overlaid")
	}

	if path == "" {
		t.Error("PATH is empty: Env overlay must not replace the base environment")
	}
}

func TestWorkdir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	cmd := invoke.New("pwd")
	cmd.Dir = dir

	_, stdout, _, err := run(t, cmd)
	if err != nil {
		t.Fatalf("Wait() = %v", err)
	}

	// Resolve symlinks on both sides: macOS puts temp dirs behind
	// /private, so literal string equality would be wrong.
	wantDir, _ := filepath.EvalSymlinks(dir)
	gotDir, _ := filepath.EvalSymlinks(strings.TrimSpace(stdout))

	if gotDir != wantDir {
		t.Errorf("pwd = %q, want %q", gotDir, wantDir)
	}
}

func TestInvalidWorkdir(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	cmd := invoke.New("true")
	cmd.Dir = filepath.Join(t.TempDir(), "does-not-exist")

	_, err := env.Start(t.Context(), cmd, invoke.IO{})
	if !errors.Is(err, invoke.ErrInvalidWorkdir) {
		t.Errorf("Start with bad Dir = %v, want ErrInvalidWorkdir", err)
	}
}

func TestMissingBinaryIsNotFound(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	if _, err := env.Start(t.Context(), invoke.New("definitely-not-a-real-binary-abc123"), invoke.IO{}); !errors.Is(err, invoke.ErrNotFound) {
		t.Errorf("Start(missing bare name) = %v, want ErrNotFound", err)
	}

	missingPath := filepath.Join(t.TempDir(), "nope")
	if _, err := env.Start(t.Context(), invoke.New(missingPath), invoke.IO{}); !errors.Is(err, invoke.ErrNotFound) {
		t.Errorf("Start(missing path) = %v, want ErrNotFound", err)
	}
}

func TestTTYIsNotSupported(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	_, err := env.Start(t.Context(), invoke.New("true"), invoke.IO{TTY: &invoke.TTY{}})
	if !errors.Is(err, invoke.ErrNotSupported) {
		t.Errorf("Start with TTY = %v, want ErrNotSupported", err)
	}
}

func TestWaitIsIdempotent(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	proc, err := env.Start(t.Context(), invoke.Shell("exit 19"), invoke.IO{})
	if err != nil {
		t.Fatalf("Start = %v", err)
	}

	res1, err1 := proc.Wait()
	res2, err2 := proc.Wait()

	if res1 != res2 {
		t.Errorf("repeated Wait results differ: %+v vs %+v", res1, res2)
	}

	var exit1, exit2 *invoke.ExitError
	if !errors.As(err1, &exit1) || !errors.As(err2, &exit2) || exit1.Code != exit2.Code {
		t.Errorf("repeated Wait errors differ: %v vs %v", err1, err2)
	}
}

func TestCancellationKillsTheProcess(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	marker := filepath.Join(t.TempDir(), "marker")
	ctx, cancel := context.WithCancel(t.Context())

	// The command creates the marker after a short sleep; a real kill
	// means the marker never appears.
	proc, err := env.Start(ctx, invoke.Shell("sleep 1 && touch "+marker), invoke.IO{})
	if err != nil {
		t.Fatalf("Start = %v", err)
	}

	cancel()

	_, waitErr := proc.Wait()
	if !errors.Is(waitErr, context.Canceled) {
		t.Errorf("Wait after cancel = %v, want context.Canceled", waitErr)
	}

	var exitErr *invoke.ExitError
	if errors.As(waitErr, &exitErr) {
		t.Errorf("cancellation surfaced as ExitError %v; lifecycle errors must not", exitErr)
	}

	// Give a surviving process ample time to prove itself, then check
	// the kill was real.
	time.Sleep(1500 * time.Millisecond)

	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("marker %q exists; cancellation did not kill the process tree", marker)
	}
}

func TestCancelAfterNaturalExitKeepsRealOutcome(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	ctx, cancel := context.WithCancel(t.Context())

	proc, err := env.Start(ctx, invoke.New("true"), invoke.IO{})
	if err != nil {
		t.Fatalf("Start = %v", err)
	}

	// Let the process exit on its own, then cancel before Wait: the
	// real outcome must win over the stale cancellation.
	time.Sleep(500 * time.Millisecond)
	cancel()

	res, waitErr := proc.Wait()
	if waitErr != nil {
		t.Errorf("Wait = %v, want success: process exited before cancellation", waitErr)
	}

	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
}

func TestStartOnCanceledContext(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := env.Start(ctx, invoke.New("true"), invoke.IO{}); !errors.Is(err, context.Canceled) {
		t.Errorf("Start on canceled ctx = %v, want context.Canceled", err)
	}
}

func TestCloseKillsAndWaitReportsClosed(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	proc, err := env.Start(t.Context(), invoke.New("sleep", "30"), invoke.IO{})
	if err != nil {
		t.Fatalf("Start = %v", err)
	}

	begun := time.Now()

	if err := proc.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}

	if elapsed := time.Since(begun); elapsed > promptly {
		t.Errorf("Close took %v, want prompt return", elapsed)
	}

	_, waitErr := proc.Wait()
	if !errors.Is(waitErr, invoke.ErrClosed) {
		t.Errorf("Wait after Close = %v, want ErrClosed", waitErr)
	}

	var exitErr *invoke.ExitError
	if errors.As(waitErr, &exitErr) {
		t.Errorf("Close surfaced as ExitError %v; lifecycle errors must not", exitErr)
	}

	if err := proc.Close(); err != nil {
		t.Errorf("second Close = %v, want nil", err)
	}
}

func TestCloseAfterWaitKeepsOutcome(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	proc, err := env.Start(t.Context(), invoke.New("true"), invoke.IO{})
	if err != nil {
		t.Fatalf("Start = %v", err)
	}

	res1, err1 := proc.Wait()
	if err1 != nil {
		t.Fatalf("Wait = %v", err1)
	}

	_ = proc.Close()

	res2, err2 := proc.Wait()
	if err2 != nil || res2 != res1 {
		t.Errorf("Wait after Close changed the outcome: (%+v, %v) vs (%+v, %v)", res2, err2, res1, err1)
	}
}

func TestCloseUnblocksConcurrentWait(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	proc, err := env.Start(t.Context(), invoke.New("sleep", "30"), invoke.IO{})
	if err != nil {
		t.Fatalf("Start = %v", err)
	}

	done := waitInBackground(proc)

	select {
	case err := <-done:
		t.Fatalf("Wait returned before Close: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	_ = proc.Close()

	select {
	case err := <-done:
		if !errors.Is(err, invoke.ErrClosed) {
			t.Errorf("unblocked Wait = %v, want ErrClosed", err)
		}
	case <-time.After(promptly):
		t.Fatal("Wait still blocked after Close")
	}
}

func TestEnvironmentCloseTerminatesRunningProcesses(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	proc, err := env.Start(t.Context(), invoke.New("sleep", "30"), invoke.IO{})
	if err != nil {
		t.Fatalf("Start = %v", err)
	}

	if err := env.Close(); err != nil {
		t.Fatalf("env.Close = %v", err)
	}

	select {
	case err := <-waitInBackground(proc):
		if !errors.Is(err, invoke.ErrClosed) {
			t.Errorf("Wait after env.Close = %v, want ErrClosed", err)
		}
	case <-time.After(promptly):
		t.Fatal("process still running after environment Close")
	}
}

func TestSignalTerminatesProcess(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	proc, err := env.Start(t.Context(), invoke.New("sleep", "30"), invoke.IO{})
	if err != nil {
		t.Fatalf("Start = %v", err)
	}

	if err := proc.Signal(invoke.SIGTERM); err != nil {
		t.Fatalf("Signal(TERM) = %v", err)
	}

	select {
	case <-waitInBackground(proc):
	case <-time.After(promptly):
		t.Fatal("process ignored SIGTERM past the deadline")
	}

	_, waitErr := proc.Wait()

	var exitErr *invoke.ExitError
	if !errors.As(waitErr, &exitErr) {
		t.Fatalf("Wait after signal = %v, want *ExitError", waitErr)
	}

	if exitErr.Signal != invoke.SIGTERM || exitErr.Code != -1 {
		t.Errorf("ExitError = {Code:%d Signal:%q}, want {Code:-1 Signal:TERM}", exitErr.Code, exitErr.Signal)
	}
}

func TestSignalAfterExit(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	proc, err := env.Start(t.Context(), invoke.New("true"), invoke.IO{})
	if err != nil {
		t.Fatalf("Start = %v", err)
	}

	if _, err := proc.Wait(); err != nil {
		t.Fatalf("Wait = %v", err)
	}

	if err := proc.Signal(invoke.SIGTERM); err == nil {
		t.Error("Signal after exit = nil, want error")
	}
}

func TestUnknownSignalIsNotSupported(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	proc, err := env.Start(t.Context(), invoke.New("sleep", "30"), invoke.IO{})
	if err != nil {
		t.Fatalf("Start = %v", err)
	}

	defer func() { _ = proc.Close() }()

	if err := proc.Signal(invoke.Signal("WINCH")); !errors.Is(err, invoke.ErrNotSupported) {
		t.Errorf("Signal(WINCH) = %v, want ErrNotSupported", err)
	}
}

// blockingReader blocks Read until the test finishes, simulating a caller
// stdin (a terminal, an idle socket) that never produces data.
type blockingReader struct{ unblock chan struct{} }

func (r *blockingReader) Read(_ []byte) (int, error) {
	<-r.unblock

	return 0, os.ErrClosed
}

func TestBlockingStdinCannotHangWait(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	reader := &blockingReader{unblock: make(chan struct{})}

	t.Cleanup(func() { close(reader.unblock) })

	proc, err := env.Start(t.Context(), invoke.New("true"), invoke.IO{Stdin: reader})
	if err != nil {
		t.Fatalf("Start = %v", err)
	}

	begun := time.Now()

	res, waitErr := proc.Wait()
	if waitErr != nil {
		t.Fatalf("Wait = %v, want success: the process exited 0", waitErr)
	}

	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}

	if elapsed := time.Since(begun); elapsed > promptly {
		t.Errorf("Wait blocked %v on a stuck stdin; the wait grace period must bound it", elapsed)
	}
}

func TestOrphanHoldingPipeCannotHangWait(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	var stdout bytes.Buffer

	// The background sleep inherits the stdout pipe and outlives the
	// shell; Wait must return once the command itself has exited, not
	// when the orphan finally releases the pipe.
	proc, err := env.Start(t.Context(), invoke.Shell("sleep 30 & echo started"), invoke.IO{Stdout: &stdout})
	if err != nil {
		t.Fatalf("Start = %v", err)
	}

	begun := time.Now()

	res, waitErr := proc.Wait()
	if waitErr != nil {
		t.Fatalf("Wait = %v, want success", waitErr)
	}

	if elapsed := time.Since(begun); elapsed > promptly {
		t.Errorf("Wait blocked %v behind an orphaned pipe holder", elapsed)
	}

	if res.ExitCode != 0 || !strings.Contains(stdout.String(), "started") {
		t.Errorf("exit=%d stdout=%q, want 0 with output captured", res.ExitCode, stdout.String())
	}
}

func TestConcurrentRunsAreIndependent(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	const workers = 5

	errs := make(chan error, workers)

	for i := range workers {
		go func() {
			var stdout bytes.Buffer

			proc, err := env.Start(t.Context(), invoke.New("echo", "worker"), invoke.IO{Stdout: &stdout})
			if err != nil {
				errs <- err

				return
			}

			_, err = proc.Wait()
			errs <- err

			_ = i
		}()
	}

	for range workers {
		if err := <-errs; err != nil {
			t.Errorf("concurrent run failed: %v", err)
		}
	}
}
