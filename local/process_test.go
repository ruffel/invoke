package local_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	require.NoError(t, err, "Start(%v)", cmd)

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
	require.NoError(t, err, "Wait()")

	assert.Equal(t, 0, res.ExitCode, "ExitCode")
	assert.Equal(t, "hello world\n", stdout, "stdout")
	assert.Empty(t, stderr, "stderr")
}

func TestStreamsStaySeparate(t *testing.T) {
	t.Parallel()

	_, stdout, stderr, err := run(t, invoke.Shell("echo out; echo err 1>&2"))
	require.NoError(t, err, "Wait()")

	assert.Equal(t, "out\n", stdout, "want separated streams")
	assert.Equal(t, "err\n", stderr, "want separated streams")
}

func TestNonZeroExitIsExitError(t *testing.T) {
	t.Parallel()

	res, _, _, err := run(t, invoke.Shell("exit 19"))

	var exitErr *invoke.ExitError

	require.ErrorAs(t, err, &exitErr, "Wait() = %v, want *ExitError", err)

	assert.Equal(t, wantExitCode, exitErr.Code, "ExitError.Code")
	assert.Empty(t, exitErr.Signal, "ExitError.Signal")
	assert.Equal(t, wantExitCode, res.ExitCode, "Result.ExitCode")
	assert.Positive(t, res.Duration, "Result.Duration")
}

func TestNilStdinIsImmediateEOF(t *testing.T) {
	t.Parallel()

	// cat with no stdin wiring must see EOF and exit, not inherit the
	// test process's stdin or hang.
	res, stdout, _, err := run(t, invoke.New("cat"))
	require.NoError(t, err, "Wait()")

	assert.Equal(t, 0, res.ExitCode, "cat with nil stdin")
	assert.Empty(t, stdout, "cat with nil stdin")
}

func TestStdinIsDelivered(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	var stdout bytes.Buffer

	proc, err := env.Start(t.Context(), invoke.New("cat"), invoke.IO{
		Stdin:  strings.NewReader("piped through"),
		Stdout: &stdout,
	})
	require.NoError(t, err, "Start")

	_, err = proc.Wait()
	require.NoError(t, err, "Wait()")

	assert.Equal(t, "piped through", stdout.String(), "stdout")
}

func TestLargeOutputDoesNotDeadlock(t *testing.T) {
	t.Parallel()

	const wantBytes = 256 * 1024

	_, stdout, _, err := run(t, invoke.Shell("dd if=/dev/zero bs=1024 count=256 2>/dev/null"))
	require.NoError(t, err, "Wait()")

	assert.Len(t, stdout, wantBytes, "captured bytes")
}

func TestEnvOverlaysBaseEnvironment(t *testing.T) {
	t.Parallel()

	cmd := invoke.Shell(`printf '%s|%s' "$INVOKE_TEST_VALUE" "$PATH"`)
	cmd.Env = []string{"INVOKE_TEST_VALUE=overlaid"}

	_, stdout, _, err := run(t, cmd)
	require.NoError(t, err, "Wait()")

	value, path, _ := strings.Cut(stdout, "|")
	assert.Equal(t, "overlaid", value, "overlay variable")
	assert.NotEmpty(t, path, "PATH is empty: Env overlay must not replace the base environment")
}

func TestWorkdir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	cmd := invoke.New("pwd")
	cmd.Dir = dir

	_, stdout, _, err := run(t, cmd)
	require.NoError(t, err, "Wait()")

	// Resolve symlinks on both sides: macOS puts temp dirs behind
	// /private, so literal string equality would be wrong.
	wantDir, _ := filepath.EvalSymlinks(dir)
	gotDir, _ := filepath.EvalSymlinks(strings.TrimSpace(stdout))

	assert.Equal(t, wantDir, gotDir, "pwd")
}

func TestInvalidWorkdir(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	cmd := invoke.New("true")
	cmd.Dir = filepath.Join(t.TempDir(), "does-not-exist")

	_, err := env.Start(t.Context(), cmd, invoke.IO{})
	assert.ErrorIs(t, err, invoke.ErrInvalidWorkdir, "Start with bad Dir")
}

func TestMissingBinaryIsNotFound(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	_, err := env.Start(t.Context(), invoke.New("definitely-not-a-real-binary-abc123"), invoke.IO{})
	assert.ErrorIs(t, err, invoke.ErrNotFound, "Start(missing bare name)")

	missingPath := filepath.Join(t.TempDir(), "nope")

	_, err = env.Start(t.Context(), invoke.New(missingPath), invoke.IO{})
	assert.ErrorIs(t, err, invoke.ErrNotFound, "Start(missing path)")
}

func TestTTYIsNotSupported(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	_, err := env.Start(t.Context(), invoke.New("true"), invoke.IO{TTY: &invoke.TTY{}})
	assert.ErrorIs(t, err, invoke.ErrNotSupported, "Start with TTY")
}

func TestWaitIsIdempotent(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	proc, err := env.Start(t.Context(), invoke.Shell("exit 19"), invoke.IO{})
	require.NoError(t, err, "Start")

	res1, err1 := proc.Wait()
	res2, err2 := proc.Wait()

	assert.Equal(t, res1, res2, "repeated Wait results differ")

	var exit1, exit2 *invoke.ExitError

	// Required, not asserted: a non-ExitError here leaves exit1 nil, and
	// the comparison below would panic rather than report.
	require.ErrorAs(t, err1, &exit1, "the first Wait must report an ExitError")
	require.ErrorAs(t, err2, &exit2, "the second Wait must report an ExitError too")

	assert.Equal(t, exit1.Code, exit2.Code, "repeated Wait calls must report the same exit code")
}

func TestCancellationKillsTheProcess(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	marker := filepath.Join(t.TempDir(), "marker")
	ctx, cancel := context.WithCancel(t.Context())

	// The command creates the marker after a short sleep; a real kill
	// means the marker never appears.
	proc, err := env.Start(ctx, invoke.Shell("sleep 1 && touch "+marker), invoke.IO{})
	require.NoError(t, err, "Start")

	cancel()

	_, waitErr := proc.Wait()
	assert.ErrorIs(t, waitErr, context.Canceled, "Wait after cancel")

	var exitErr *invoke.ExitError

	assert.NotErrorAs(t, waitErr, &exitErr, "cancellation surfaced as ExitError; lifecycle errors must not")

	// Give a surviving process ample time to prove itself, then check
	// the kill was real.
	time.Sleep(1500 * time.Millisecond)

	_, err = os.Stat(marker)
	assert.ErrorIs(t, err, os.ErrNotExist, "marker %q exists; cancellation did not kill the process tree", marker)
}

func TestCancelAfterNaturalExitKeepsRealOutcome(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	ctx, cancel := context.WithCancel(t.Context())

	proc, err := env.Start(ctx, invoke.New("true"), invoke.IO{})
	require.NoError(t, err, "Start")

	// Let the process exit on its own, then cancel before Wait: the
	// real outcome must win over the stale cancellation.
	time.Sleep(500 * time.Millisecond)
	cancel()

	res, waitErr := proc.Wait()
	assert.NoError(t, waitErr, "Wait, want success: process exited before cancellation")

	assert.Equal(t, 0, res.ExitCode, "ExitCode")
}

func TestStartOnCanceledContext(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := env.Start(ctx, invoke.New("true"), invoke.IO{})
	assert.ErrorIs(t, err, context.Canceled, "Start on canceled ctx")
}

func TestCloseKillsAndWaitReportsClosed(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	proc, err := env.Start(t.Context(), invoke.New("sleep", "30"), invoke.IO{})
	require.NoError(t, err, "Start")

	begun := time.Now()

	require.NoError(t, proc.Close(), "Close")

	elapsed := time.Since(begun)
	assert.LessOrEqual(t, elapsed, promptly, "Close took %v, want prompt return", elapsed)

	_, waitErr := proc.Wait()
	assert.ErrorIs(t, waitErr, invoke.ErrClosed, "Wait after Close")

	var exitErr *invoke.ExitError

	assert.NotErrorAs(t, waitErr, &exitErr, "Close surfaced as ExitError; lifecycle errors must not")

	assert.NoError(t, proc.Close(), "second Close, want nil")
}

func TestCloseAfterWaitKeepsOutcome(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	proc, err := env.Start(t.Context(), invoke.New("true"), invoke.IO{})
	require.NoError(t, err, "Start")

	res1, err1 := proc.Wait()
	require.NoError(t, err1, "Wait")

	_ = proc.Close()

	res2, err2 := proc.Wait()
	assert.NoError(t, err2, "Wait after Close changed the outcome")
	assert.Equal(t, res1, res2, "Wait after Close changed the outcome")
}

func TestCloseUnblocksConcurrentWait(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	proc, err := env.Start(t.Context(), invoke.New("sleep", "30"), invoke.IO{})
	require.NoError(t, err, "Start")

	done := waitInBackground(proc)

	select {
	case err := <-done:
		require.Failf(t, "Wait returned before Close", "err = %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	_ = proc.Close()

	select {
	case err := <-done:
		assert.ErrorIs(t, err, invoke.ErrClosed, "unblocked Wait")
	case <-time.After(promptly):
		require.Fail(t, "Wait still blocked after Close")
	}
}

func TestEnvironmentCloseTerminatesRunningProcesses(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	proc, err := env.Start(t.Context(), invoke.New("sleep", "30"), invoke.IO{})
	require.NoError(t, err, "Start")

	require.NoError(t, env.Close(), "env.Close")

	select {
	case err := <-waitInBackground(proc):
		assert.ErrorIs(t, err, invoke.ErrClosed, "Wait after env.Close")
	case <-time.After(promptly):
		require.Fail(t, "process still running after environment Close")
	}
}

func TestSignalTerminatesProcess(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	proc, err := env.Start(t.Context(), invoke.New("sleep", "30"), invoke.IO{})
	require.NoError(t, err, "Start")

	require.NoError(t, proc.Signal(invoke.SIGTERM), "Signal(TERM)")

	select {
	case <-waitInBackground(proc):
	case <-time.After(promptly):
		require.Fail(t, "process ignored SIGTERM past the deadline")
	}

	_, waitErr := proc.Wait()

	var exitErr *invoke.ExitError

	require.ErrorAs(t, waitErr, &exitErr, "Wait after signal = %v, want *ExitError", waitErr)

	assert.Equal(t, invoke.SIGTERM, exitErr.Signal, "ExitError.Signal")
	assert.Equal(t, -1, exitErr.Code, "ExitError.Code")
}

func TestSignalAfterExit(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	proc, err := env.Start(t.Context(), invoke.New("true"), invoke.IO{})
	require.NoError(t, err, "Start")

	_, err = proc.Wait()
	require.NoError(t, err, "Wait")

	assert.Error(t, proc.Signal(invoke.SIGTERM), "Signal after exit = nil, want error")
}

func TestUnknownSignalIsNotSupported(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	proc, err := env.Start(t.Context(), invoke.New("sleep", "30"), invoke.IO{})
	require.NoError(t, err, "Start")

	defer func() { _ = proc.Close() }()

	assert.ErrorIs(t, proc.Signal(invoke.Signal("WINCH")), invoke.ErrNotSupported, "Signal(WINCH)")
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
	require.NoError(t, err, "Start")

	begun := time.Now()

	res, waitErr := proc.Wait()
	require.NoError(t, waitErr, "Wait, want success: the process exited 0")

	assert.Equal(t, 0, res.ExitCode, "ExitCode")

	elapsed := time.Since(begun)
	assert.LessOrEqual(t, elapsed, promptly,
		"Wait blocked %v on a stuck stdin; the wait grace period must bound it", elapsed)
}

func TestOrphanHoldingPipeCannotHangWait(t *testing.T) {
	t.Parallel()

	env := newEnv(t)

	var stdout bytes.Buffer

	// The background sleep inherits the stdout pipe and outlives the
	// shell; Wait must return once the command itself has exited, not
	// when the orphan finally releases the pipe.
	proc, err := env.Start(t.Context(), invoke.Shell("sleep 30 & echo started"), invoke.IO{Stdout: &stdout})
	require.NoError(t, err, "Start")

	begun := time.Now()

	res, waitErr := proc.Wait()
	require.NoError(t, waitErr, "Wait, want success")

	elapsed := time.Since(begun)
	assert.LessOrEqual(t, elapsed, promptly, "Wait blocked %v behind an orphaned pipe holder", elapsed)

	assert.Equal(t, 0, res.ExitCode, "want 0 with output captured")
	assert.Contains(t, stdout.String(), "started", "want 0 with output captured")
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
		assert.NoError(t, <-errs, "concurrent run failed")
	}
}
