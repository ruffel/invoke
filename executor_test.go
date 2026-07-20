package invoke_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/fake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scriptEnv is a minimal Environment whose Start returns pre-scripted
// outcomes in order, so retry classification can be tested against
// TransportError — which the fake provider (a healthy target) cannot
// produce. Each script entry becomes one process whose Wait returns it.
type scriptEnv struct {
	mu      sync.Mutex
	starts  int
	outcome []scriptOutcome
}

type scriptOutcome struct {
	result   invoke.Result
	err      error
	startErr error  // returned from Start instead of a process
	onStdout string // written to the attempt's Stdout before Wait
}

func (s *scriptEnv) Start(_ context.Context, _ invoke.Command, stdio invoke.IO) (invoke.Process, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.starts >= len(s.outcome) {
		return nil, fmt.Errorf("scriptEnv: unexpected Start #%d", s.starts+1)
	}

	out := s.outcome[s.starts]
	s.starts++

	if out.onStdout != "" && stdio.Stdout != nil {
		_, _ = stdio.Stdout.Write([]byte(out.onStdout))
	}

	if out.startErr != nil {
		return nil, out.startErr
	}

	return &scriptProcess{result: out.result, err: out.err}, nil
}

func (s *scriptEnv) LookPath(_ context.Context, name string) (string, error) { return name, nil }

func (s *scriptEnv) Upload(_ context.Context, _, _ string, _ ...invoke.TransferOption) error {
	return nil
}

func (s *scriptEnv) Download(_ context.Context, _, _ string, _ ...invoke.TransferOption) error {
	return nil
}

func (s *scriptEnv) OS() invoke.TargetOS               { return invoke.OSLinux }
func (s *scriptEnv) Capabilities() invoke.Capabilities { return invoke.Capabilities{} }
func (s *scriptEnv) Close() error                      { return nil }

func (s *scriptEnv) startCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.starts
}

type scriptProcess struct {
	result invoke.Result
	err    error
}

func (p *scriptProcess) Wait() (invoke.Result, error) { return p.result, p.err }
func (p *scriptProcess) Signal(invoke.Signal) error   { return nil }
func (p *scriptProcess) Close() error                 { return nil }

func transportErr() error {
	return &invoke.TransportError{Op: "start", Err: errors.New("connection reset")}
}

func TestRunNoRetryByDefault(t *testing.T) {
	t.Parallel()

	env := &scriptEnv{outcome: []scriptOutcome{
		{err: transportErr()},
	}}

	exec := invoke.NewExecutor(env)

	_, err := exec.Run(t.Context(), invoke.New("x"), invoke.IO{})
	require.ErrorAs(t, err, new(*invoke.TransportError))

	assert.Equal(t, 1, env.startCount(), "no retry by default")
}

func TestRetryOnlyTransportErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        error
		wantStarts int
	}{
		{name: "transport error retries then exhausts", err: transportErr(), wantStarts: 3},
		{name: "exit error is terminal", err: &invoke.ExitError{Code: 1}, wantStarts: 1},
		{name: "closed is terminal", err: fmt.Errorf("x: %w", invoke.ErrClosed), wantStarts: 1},
		{name: "canceled is terminal", err: fmt.Errorf("x: %w", context.Canceled), wantStarts: 1},
		{name: "not-found is terminal", err: fmt.Errorf("x: %w", invoke.ErrNotFound), wantStarts: 1},
		{name: "unsupported is terminal", err: fmt.Errorf("x: %w", invoke.ErrNotSupported), wantStarts: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			outcomes := make([]scriptOutcome, 3)
			for i := range outcomes {
				outcomes[i] = scriptOutcome{err: tt.err}
			}

			env := &scriptEnv{outcome: outcomes}
			exec := invoke.NewExecutor(env, invoke.WithRetry(3, nil))

			_, err := exec.Run(t.Context(), invoke.New("x"), invoke.IO{})

			matched := errors.Is(err, tt.err) || errors.As(err, new(*invoke.TransportError))

			assert.True(t, matched, "Run err = %v", err)
			assert.Equal(t, tt.wantStarts, env.startCount())
		})
	}
}

// TestTerminalOutcomeWrappedInTransportIsNotRetried pins the rule that
// decides whether an arbitrary command may be run a second time.
//
// A TransportError unwraps, so one wrapping a terminal outcome satisfies
// every assertion about the outcome it carries while still answering to
// the retryable family. A provider that reports a command's own verdict in
// that shape would otherwise have it executed again.
func TestTerminalOutcomeWrappedInTransportIsNotRetried(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		inner error
	}{
		{name: "exit error", inner: &invoke.ExitError{Code: 1}},
		{name: "closed", inner: fmt.Errorf("x: %w", invoke.ErrClosed)},
		{name: "not found", inner: fmt.Errorf("x: %w", invoke.ErrNotFound)},
		{name: "unsupported", inner: fmt.Errorf("x: %w", invoke.ErrNotSupported)},
		{name: "invalid workdir", inner: fmt.Errorf("x: %w", invoke.ErrInvalidWorkdir)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			outcomes := make([]scriptOutcome, 3)
			for i := range outcomes {
				outcomes[i] = scriptOutcome{err: &invoke.TransportError{Op: "wait", Err: tt.inner}}
			}

			env := &scriptEnv{outcome: outcomes}
			exec := invoke.NewExecutor(env, invoke.WithRetry(3, nil))

			_, err := exec.Run(t.Context(), invoke.New("deploy", "--production"), invoke.IO{})
			require.Error(t, err)

			assert.Equal(t, 1, env.startCount(),
				"a command whose outcome was already decided was run again under retry")
		})
	}
}

// TestTransferCarryingATerminalOutcomeIsNotRetried covers the other path
// through the same classifier: transfers retry under their own loop.
func TestTransferCarryingATerminalOutcomeIsNotRetried(t *testing.T) {
	t.Parallel()

	env := &transferEnv{
		failFirst: 2,
		err:       &invoke.TransportError{Op: "upload", Err: fmt.Errorf("x: %w", invoke.ErrNotFound)},
	}
	exec := invoke.NewExecutor(env, invoke.WithRetry(3, nil))

	require.Error(t, exec.Upload(t.Context(), "src", "dst"))

	assert.Equal(t, 1, env.uploads, "a transfer whose failure was already decided was attempted again")
}

func TestRetrySucceedsAfterTransientFailure(t *testing.T) {
	t.Parallel()

	env := &scriptEnv{outcome: []scriptOutcome{
		{err: transportErr()},
		{result: invoke.Result{ExitCode: 0}},
	}}

	exec := invoke.NewExecutor(env, invoke.WithRetry(3, nil))

	res, err := exec.Run(t.Context(), invoke.New("x"), invoke.IO{})
	require.NoError(t, err, "Run must succeed on the second attempt")

	assert.Equal(t, 0, res.ExitCode)
	assert.Equal(t, 2, env.startCount(), "want exit 0 after 2 starts")
}

func TestExhaustedRetryReturnsProviderErrorNotDoubleWrapped(t *testing.T) {
	t.Parallel()

	original := transportErr()
	env := &scriptEnv{outcome: []scriptOutcome{{err: original}, {err: original}}}

	exec := invoke.NewExecutor(env, invoke.WithRetry(2, nil))

	_, err := exec.Run(t.Context(), invoke.New("x"), invoke.IO{})

	var te *invoke.TransportError

	require.ErrorAs(t, err, &te)

	// The provider's error is returned as-is; it is not wrapped in a
	// second transport layer.
	assert.Equal(t, 1, strings.Count(err.Error(), "transport failure"),
		"the error was double-wrapped: %q", err.Error())
}

func TestAttemptsBelowOneIsValidationError(t *testing.T) {
	t.Parallel()

	env := &scriptEnv{}
	exec := invoke.NewExecutor(env, invoke.WithRetry(0, nil))

	_, err := exec.Run(t.Context(), invoke.New("x"), invoke.IO{})
	require.Error(t, err, "Run with 0 attempts must return a validation error")

	assert.Equal(t, 0, env.startCount(), "validation must precede execution")
}

func TestRetryWithStdinRequiresFreshIO(t *testing.T) {
	t.Parallel()

	env := &scriptEnv{outcome: []scriptOutcome{{err: transportErr()}}}
	exec := invoke.NewExecutor(env, invoke.WithRetry(3, nil))

	_, err := exec.Run(t.Context(), invoke.New("cat"), invoke.IO{Stdin: strings.NewReader("data")})
	require.ErrorContains(t, err, "WithFreshIO", "Run must return an error demanding WithFreshIO")

	assert.Equal(t, 0, env.startCount(), "the reused-stdin guard must precede execution")
}

func TestOutputDoesNotAccumulateAcrossRetries(t *testing.T) {
	t.Parallel()

	// The first attempt writes partial output then fails at transport;
	// the retry writes the real output. Output must return only the
	// successful attempt's bytes — the legacy accumulation bug.
	env := &scriptEnv{outcome: []scriptOutcome{
		{onStdout: "PARTIAL-FROM-ATTEMPT-1", err: transportErr()},
		{onStdout: "clean-attempt-2", result: invoke.Result{ExitCode: 0}},
	}}

	exec := invoke.NewExecutor(env, invoke.WithRetry(3, nil))

	_, stdout, _, err := exec.Output(t.Context(), invoke.New("x"))
	require.NoError(t, err)

	assert.Equal(t, "clean-attempt-2", string(stdout), "Output must return only the successful attempt's output")
}

func TestBackoffIsCancelable(t *testing.T) {
	t.Parallel()

	env := &scriptEnv{outcome: []scriptOutcome{{err: transportErr()}, {err: transportErr()}}}
	exec := invoke.NewExecutor(env, invoke.WithRetry(3, invoke.ConstantBackoff(30*time.Second)))

	ctx, cancel := context.WithCancel(t.Context())

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	begun := time.Now()

	_, err := exec.Run(ctx, invoke.New("x"), invoke.IO{})

	assert.ErrorIs(t, err, context.Canceled, "an interrupted backoff must surface the context's own error")

	assert.LessOrEqual(t, time.Since(begun), 5*time.Second, "cancellation must interrupt the backoff")
}

func TestSudoArgvConstruction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts []invoke.SudoOption
		cmd  invoke.Command
		want []string
	}{
		{
			name: "bare sudo",
			cmd:  invoke.New("ls", "/root"),
			want: []string{"-n", "--", "ls", "/root"},
		},
		{
			name: "user and group",
			opts: []invoke.SudoOption{invoke.WithSudoUser("deploy"), invoke.WithSudoGroup("web")},
			cmd:  invoke.New("systemctl", "restart", "app"),
			want: []string{"-n", "-u", "deploy", "-g", "web", "--", "systemctl", "restart", "app"},
		},
		{
			name: "preserve env",
			opts: []invoke.SudoOption{invoke.WithSudoPreserveEnv()},
			cmd:  invoke.New("env"),
			want: []string{"-n", "-E", "--", "env"},
		},
		{
			name: "command starting with a dash stays after the separator",
			cmd:  invoke.New("-rf", "/"),
			want: []string{"-n", "--", "-rf", "/"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var seen invoke.Command

			env := &captureEnv{onStart: func(c invoke.Command) { seen = c }}
			exec := invoke.NewExecutor(env)

			_, err := exec.Run(t.Context(), tt.cmd, invoke.IO{}, invoke.WithSudo(tt.opts...))
			require.NoError(t, err)

			assert.Equal(t, "sudo", seen.Path)
			assert.Equal(t, tt.want, seen.Args)
		})
	}
}

func TestOutputAgainstFakeProvider(t *testing.T) {
	t.Parallel()

	env := fake.New()

	t.Cleanup(func() { _ = env.Close() })

	exec := invoke.NewExecutor(env)

	res, stdout, stderr, err := exec.Output(t.Context(), invoke.Shell("echo out; echo err 1>&2"))
	require.NoError(t, err)

	assert.Equal(t, 0, res.ExitCode)
	assert.Equal(t, "out\n", string(stdout))
	assert.Equal(t, "err\n", string(stderr))
}

func TestOutputAttachesStderrToExitError(t *testing.T) {
	t.Parallel()

	env := fake.New()

	t.Cleanup(func() { _ = env.Close() })

	exec := invoke.NewExecutor(env)

	_, _, _, err := exec.Output(t.Context(), invoke.Shell("echo boom 1>&2; exit 2")) //nolint:dogsled // Output returns four values; only the error matters here.

	var exitErr *invoke.ExitError

	require.ErrorAs(t, err, &exitErr)

	assert.Contains(t, string(exitErr.Stderr), "boom", "ExitError.Stderr must carry the captured stderr")
}

func TestLinesDeliversEachLine(t *testing.T) {
	t.Parallel()

	env := fake.New()

	t.Cleanup(func() { _ = env.Close() })

	exec := invoke.NewExecutor(env)

	var lines []string

	res, err := exec.Lines(t.Context(), invoke.Shell("printf 'a\\nb\\nc\\n'"), func(line string) {
		lines = append(lines, line)
	})
	require.NoError(t, err)

	assert.Equal(t, 0, res.ExitCode)
	assert.Equal(t, []string{"a", "b", "c"}, lines)
}

func TestExecutorUploadRetriesTransportErrors(t *testing.T) {
	t.Parallel()

	env := &transferEnv{failFirst: 2, err: transportErr()}
	exec := invoke.NewExecutor(env, invoke.WithRetry(3, nil))

	err := exec.Upload(t.Context(), "src", "dst")
	require.NoError(t, err, "Upload must succeed after transient transport failures")

	assert.Equal(t, 3, env.uploads)
}

// captureEnv records the Command passed to Start and returns success.
type captureEnv struct {
	onStart func(invoke.Command)
}

func (c *captureEnv) Start(_ context.Context, cmd invoke.Command, _ invoke.IO) (invoke.Process, error) {
	c.onStart(cmd)

	return &scriptProcess{result: invoke.Result{ExitCode: 0}}, nil
}

func (c *captureEnv) LookPath(_ context.Context, name string) (string, error) { return name, nil }

func (c *captureEnv) Upload(_ context.Context, _, _ string, _ ...invoke.TransferOption) error {
	return nil
}

func (c *captureEnv) Download(_ context.Context, _, _ string, _ ...invoke.TransferOption) error {
	return nil
}

func (c *captureEnv) OS() invoke.TargetOS               { return invoke.OSLinux }
func (c *captureEnv) Capabilities() invoke.Capabilities { return invoke.Capabilities{} }
func (c *captureEnv) Close() error                      { return nil }

// transferEnv fails the first failFirst uploads with err, then succeeds.
type transferEnv struct {
	failFirst int
	err       error
	uploads   int
}

func (e *transferEnv) Start(context.Context, invoke.Command, invoke.IO) (invoke.Process, error) {
	return nil, errors.New("unused")
}

func (e *transferEnv) LookPath(context.Context, string) (string, error) { return "", nil }

func (e *transferEnv) Upload(context.Context, string, string, ...invoke.TransferOption) error {
	e.uploads++
	if e.uploads <= e.failFirst {
		return e.err
	}

	return nil
}

func (e *transferEnv) Download(context.Context, string, string, ...invoke.TransferOption) error {
	return nil
}

func (e *transferEnv) OS() invoke.TargetOS               { return invoke.OSLinux }
func (e *transferEnv) Capabilities() invoke.Capabilities { return invoke.Capabilities{} }
func (e *transferEnv) Close() error                      { return nil }
