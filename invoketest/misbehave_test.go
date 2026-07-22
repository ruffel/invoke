package invoketest

// The misbehave harness proves every contract can fail. It wraps the local
// provider — the reference implementation — and injects one specific
// defect at a time; the self-test matrix asserts that each contract fails
// against its defect and passes against the clean reference. A contract
// with no registered defect fails the matrix outright: a test that cannot
// fail is not a test.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/local"
	"github.com/stretchr/testify/require"
)

// defects enumerates the injectable misbehaviors. Each field breaks
// exactly one promise a contract enforces.
type defects struct {
	// Execution stream defects.
	truncateOutput bool // forward only the first 64 KiB of stdout
	garbleOutput   bool // prepend garbage to stdout
	mergeStreams   bool // route stderr into stdout
	swallowStdin   bool // drop the caller's Stdin entirely
	stdinNeverEOF  bool // replace nil Stdin with a never-ending reader

	// Execution semantics defects.
	dropEnv            bool // discard Command.Env
	reverseEnv         bool // reverse Command.Env so first-wins instead of last-wins
	ignoreWorkdir      bool // discard Command.Dir
	shellJoinArgs      bool // collapse argv into one shell-joined argument
	exitZeroLie        bool // report success for non-zero exits
	clampExitCode      bool // reinterpret an exit status >= 128 as a signal death
	zeroDuration       bool // report a zero Duration
	truncateStdin      bool // forward only the first 64 KiB of stdin
	lieOS              bool // swap the reported OSLinux/OSDarwin
	bareLookPath       bool // return LookPath results without a path separator
	shell127AsNotFound bool // reclassify a shell exit 127 as ErrNotFound

	// Lifecycle defects.
	ignoreCancel         bool // detach the process from the caller's context
	exitErrorOnCancel    bool // misreport cancellation as an ExitError
	cancelOverwritesExit bool // rewrite a completed success as a late cancellation
	hardcodeCanceled     bool // report every context error as Canceled, even a deadline
	collapseSignalNames  bool // report every signal death as SIGTERM
	singleProcess        bool // refuse a second concurrent Start
	closeNoOp            bool // process Close does nothing
	secondCloseErrors    bool // a second Close returns an error
	closeForgets         bool // Wait after Close loses the cached outcome
	envCloseNoOp         bool // environment Close does nothing
	silentSignal         bool // Signal reports success and delivers nothing
	plainSignalError     bool // unsupported signal errors without ErrNotSupported
	waitFlipFlops        bool // a second Wait returns a different outcome

	// Classification defects.
	plainMissingBinary bool // strip ErrNotFound from missing-binary failures
	plainWorkdirError  bool // strip ErrInvalidWorkdir from workdir failures
	plainLookPathError bool // strip ErrNotFound from LookPath failures
	launderTerminal    bool // dress a terminal outcome up as a retryable transport failure

	// Transfer defects.
	corruptUploads           bool // upload garbage in place of the source
	flattenModes             bool // force a fixed mode, ignoring source and option
	dropModeOption           bool // strip WithMode from the options
	destroyOnFailure         bool // clobber the upload destination when a transfer fails
	destroyDownloadOnFailure bool // clobber the local destination when a download fails
	shallowTrees             bool // upload only a tree's top-level files
	dropSymlinks             bool // silently skip symlinks in transfers
	followLies               bool // treat SymlinkFollow as SymlinkSkip
	alwaysSkipSpecial        bool // force WithSkipSpecial regardless of options
	zeroProgressTotals       bool // report Total as zero in progress callbacks
	transferIgnoresCancel    bool // detach a transfer from the caller's context

	// TTY defects.
	claimTTY bool // advertise the TTY capability while allocating nothing
	stripTTY bool // report no TTY support, then discard IO.TTY instead of refusing it
}

// misbehaveEnv wraps a real Environment and applies the configured
// defects.
type misbehaveEnv struct {
	base invoke.Environment
	d    defects

	mu     sync.Mutex
	active int // outstanding processes, for the singleProcess defect
}

func (m *misbehaveEnv) Start(ctx context.Context, cmd invoke.Command, stdio invoke.IO) (invoke.Process, error) {
	startCtx := ctx
	if m.d.ignoreCancel {
		startCtx = context.WithoutCancel(ctx)
	}

	if m.d.singleProcess {
		m.mu.Lock()

		if m.active > 0 {
			m.mu.Unlock()

			return nil, errors.New("misbehave: only one process at a time")
		}

		m.active++

		m.mu.Unlock()
	}

	proc, err := m.base.Start(startCtx, m.mutateCommand(cmd), m.mutateStdio(stdio))
	if err != nil {
		m.release()

		if m.d.plainMissingBinary && errors.Is(err, invoke.ErrNotFound) {
			return nil, errors.New("misbehave: something went wrong")
		}

		if m.d.plainWorkdirError && errors.Is(err, invoke.ErrInvalidWorkdir) {
			return nil, errors.New("misbehave: something went wrong")
		}

		if m.d.launderTerminal && isTerminalStartError(err) {
			return nil, &invoke.TransportError{Op: "start", Err: err}
		}

		return nil, err
	}

	return &misbehaveProcess{base: proc, d: m.d, env: m, startCtx: ctx}, nil
}

func (m *misbehaveEnv) LookPath(ctx context.Context, name string) (string, error) {
	path, err := m.base.LookPath(ctx, name)
	if err != nil && m.d.plainLookPathError && errors.Is(err, invoke.ErrNotFound) {
		return "", errors.New("misbehave: something went wrong")
	}

	if err == nil && m.d.bareLookPath {
		return filepath.Base(path), nil
	}

	return path, err
}

func (m *misbehaveEnv) Upload(ctx context.Context, localPath, remotePath string, opts ...invoke.TransferOption) error {
	if m.d.transferIgnoresCancel {
		ctx = context.WithoutCancel(ctx)
	}

	cfg := invoke.NewTransferConfig(opts...)

	if m.d.dropModeOption {
		cfg.Mode = nil
	}

	if m.d.dropSymlinks || m.d.followLies {
		cfg.Symlinks = invoke.SymlinkSkip
	}

	if m.d.alwaysSkipSpecial {
		cfg.SkipSpecial = true
	}

	if m.d.zeroProgressTotals && cfg.Progress != nil {
		forward := cfg.Progress
		cfg.Progress = func(p invoke.TransferProgress) {
			p.Total = 0
			forward(p)
		}
	}

	src := localPath

	if m.d.corruptUploads {
		corrupted, err := corruptCopyOf(localPath)
		if err != nil {
			return err
		}

		defer func() { _ = os.RemoveAll(filepath.Dir(corrupted)) }()

		src = corrupted
	}

	if m.d.shallowTrees {
		shallow, err := shallowCopyOf(localPath)
		if err == nil {
			defer func() { _ = os.RemoveAll(filepath.Dir(shallow)) }()

			src = shallow
		}
	}

	rebuilt := rebuildOpts(cfg)
	if m.d.flattenModes {
		rebuilt = append(rebuilt, invoke.WithMode(0o644))
	}

	err := m.base.Upload(ctx, src, remotePath, rebuilt...)
	if err != nil && m.d.destroyOnFailure {
		junk, junkErr := corruptCopyOf(localPath)
		if junkErr == nil {
			defer func() { _ = os.RemoveAll(filepath.Dir(junk)) }()

			_ = m.base.Upload(context.WithoutCancel(ctx), junk, remotePath)
		}
	}

	return err
}

func (m *misbehaveEnv) Download(ctx context.Context, remotePath, localPath string, opts ...invoke.TransferOption) error {
	err := m.base.Download(ctx, remotePath, localPath, opts...)
	if err != nil && m.d.destroyDownloadOnFailure {
		_ = os.WriteFile(localPath, []byte("CLOBBERED"), 0o600)
	}

	return err
}

func (m *misbehaveEnv) OS() invoke.TargetOS {
	os := m.base.OS()
	if m.d.lieOS {
		switch os {
		case invoke.OSLinux:
			return invoke.OSDarwin
		case invoke.OSDarwin:
			return invoke.OSLinux
		default:
			return invoke.OSLinux
		}
	}

	return os
}

func (m *misbehaveEnv) Capabilities() invoke.Capabilities {
	caps := m.base.Capabilities()

	if m.d.claimTTY {
		caps.TTY = true
	}

	// The defect being injected is a target that cannot allocate a
	// terminal and ignores the request instead of refusing it, so it has
	// to present itself as one — whatever the wrapped target can do.
	if m.d.stripTTY {
		caps.TTY = false
	}

	return caps
}

// rebuildOpts converts a materialized TransferConfig back into options,
// letting defects manipulate a call's configuration.
func rebuildOpts(cfg invoke.TransferConfig) []invoke.TransferOption {
	opts := []invoke.TransferOption{invoke.WithSymlinks(cfg.Symlinks)}

	if cfg.Mode != nil {
		opts = append(opts, invoke.WithMode(*cfg.Mode))
	}

	if cfg.SkipSpecial {
		opts = append(opts, invoke.WithSkipSpecial())
	}

	if cfg.Progress != nil {
		opts = append(opts, invoke.WithProgress(cfg.Progress))
	}

	return opts
}

// corruptCopyOf writes a corrupted stand-in for path in a fresh temp dir.
func corruptCopyOf(path string) (string, error) {
	dir, err := os.MkdirTemp("", "misbehave-*")
	if err != nil {
		return "", err
	}

	corrupted := filepath.Join(dir, filepath.Base(path))
	if err := os.WriteFile(corrupted, []byte("CORRUPTED"), 0o600); err != nil {
		return "", err
	}

	return corrupted, nil
}

// shallowCopyOf copies only the top-level regular files of a directory
// tree into a fresh temp dir, dropping everything nested.
func shallowCopyOf(path string) (string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", err
	}

	dir, err := os.MkdirTemp("", "misbehave-*")
	if err != nil {
		return "", err
	}

	shallow := filepath.Join(dir, filepath.Base(path))
	if err := os.Mkdir(shallow, 0o755); err != nil {
		return "", err
	}

	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}

		content, err := os.ReadFile(filepath.Join(path, entry.Name()))
		if err != nil {
			return "", err
		}

		if err := os.WriteFile(filepath.Join(shallow, entry.Name()), content, 0o600); err != nil {
			return "", err
		}
	}

	return shallow, nil
}

func (m *misbehaveEnv) Close() error {
	if m.d.envCloseNoOp {
		return nil
	}

	return m.base.Close()
}

// release drops one outstanding process from the singleProcess counter.
func (m *misbehaveEnv) release() {
	if !m.d.singleProcess {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.active > 0 {
		m.active--
	}
}

func (m *misbehaveEnv) mutateCommand(cmd invoke.Command) invoke.Command {
	if m.d.dropEnv {
		cmd.Env = nil
	}

	if m.d.reverseEnv && len(cmd.Env) > 1 {
		reversed := make([]string, len(cmd.Env))
		for i, pair := range cmd.Env {
			reversed[len(cmd.Env)-1-i] = pair
		}

		cmd.Env = reversed
	}

	if m.d.ignoreWorkdir {
		cmd.Dir = ""
	}

	if m.d.shellJoinArgs && len(cmd.Args) > 0 {
		cmd.Args = []string{strings.Join(cmd.Args, " ")}
	}

	return cmd
}

func (m *misbehaveEnv) mutateStdio(stdio invoke.IO) invoke.IO {
	if m.d.swallowStdin {
		stdio.Stdin = nil
	}

	if m.d.stdinNeverEOF && stdio.Stdin == nil {
		stdio.Stdin = neverEOFReader{}
	}

	if m.d.truncateStdin && stdio.Stdin != nil {
		stdio.Stdin = io.LimitReader(stdio.Stdin, 64*1024)
	}

	if m.d.mergeStreams && stdio.Stdout != nil {
		stdio.Stderr = stdio.Stdout
	}

	if m.d.truncateOutput && stdio.Stdout != nil {
		stdio.Stdout = &limitWriter{w: stdio.Stdout, limit: 64 * 1024}
	}

	if m.d.garbleOutput && stdio.Stdout != nil {
		stdio.Stdout = &garbleWriter{w: stdio.Stdout}
	}

	if (m.d.claimTTY || m.d.stripTTY) && stdio.TTY != nil {
		stdio.TTY = nil
	}

	return stdio
}

// misbehaveProcess applies process-level defects. It guards its own
// bookkeeping so it is as concurrency-safe as the provider it wraps, which
// the concurrent-wait contract requires.
type misbehaveProcess struct {
	base     invoke.Process
	d        defects
	env      *misbehaveEnv
	startCtx context.Context //nolint:containedctx // Mirrors the real providers, which store the start context.
	released sync.Once

	mu         sync.Mutex
	waitCalls  int
	closeCalls int
	closed     bool
}

func (p *misbehaveProcess) Wait() (invoke.Result, error) {
	// base.Wait is idempotent and concurrency-safe; call it without the
	// lock so concurrent callers do not serialize on a blocking wait.
	res, err := p.base.Wait()
	p.released.Do(p.env.release)

	p.mu.Lock()
	p.waitCalls++
	waitCall, closed := p.waitCalls, p.closed
	p.mu.Unlock()

	if got, done := p.corruptWait(res, err, waitCall, closed); done {
		return got.result, got.err
	}

	if got, done := p.corruptError(res, err); done {
		return got.result, got.err
	}

	if p.d.zeroDuration {
		res.Duration = 0
	}

	return res, err
}

func (p *misbehaveProcess) Signal(sig invoke.Signal) error {
	if p.d.silentSignal {
		return nil
	}

	if p.d.plainSignalError {
		if err := p.base.Signal(sig); err != nil {
			return errors.New("misbehave: signal failed for unstated reasons")
		}

		return nil
	}

	return p.base.Signal(sig)
}

func (p *misbehaveProcess) Close() error {
	p.mu.Lock()
	p.closeCalls++
	p.closed = true
	closeCall := p.closeCalls
	p.mu.Unlock()

	p.released.Do(p.env.release)

	if p.d.closeNoOp {
		return nil
	}

	if p.d.secondCloseErrors && closeCall > 1 {
		return errors.New("misbehave: already closed")
	}

	return p.base.Close()
}

// corruptWait applies defects that depend on Wait's call sequence or the
// process's own lifecycle bookkeeping, using a snapshot of that state.
func (p *misbehaveProcess) corruptWait(res invoke.Result, err error, waitCall int, closed bool) (waitOutcome, bool) {
	switch {
	case p.d.waitFlipFlops && waitCall > 1:
		return waitOutcome{result: invoke.Result{ExitCode: res.ExitCode + 1, Duration: res.Duration}}, true
	case p.d.closeForgets && closed:
		return waitOutcome{err: errors.New("misbehave: outcome discarded by close")}, true
	case p.d.cancelOverwritesExit && err == nil && p.startCtx.Err() != nil:
		return waitOutcome{
			result: invoke.Result{ExitCode: -1, Duration: res.Duration},
			err:    fmt.Errorf("misbehave: %w", p.startCtx.Err()),
		}, true
	default:
		return waitOutcome{}, false
	}
}

// corruptError applies defects that rewrite the classification of a
// finished process's error.
func (p *misbehaveProcess) corruptError(res invoke.Result, err error) (waitOutcome, bool) {
	const signalBoundary = 128

	switch {
	case p.d.exitZeroLie && asExitError(err) != nil:
		return waitOutcome{result: invoke.Result{ExitCode: 0, Duration: res.Duration}}, true
	case p.d.exitErrorOnCancel && errors.Is(err, context.Canceled):
		return waitOutcome{result: res, err: &invoke.ExitError{Code: -1}}, true
	case p.d.hardcodeCanceled && errors.Is(err, context.DeadlineExceeded):
		return waitOutcome{result: res, err: fmt.Errorf("misbehave: %w", context.Canceled)}, true
	}

	exitErr := asExitError(err)
	if exitErr == nil {
		return waitOutcome{}, false
	}

	switch {
	case p.d.launderTerminal:
		return waitOutcome{result: res, err: &invoke.TransportError{Op: "wait", Err: err}}, true
	case p.d.collapseSignalNames && exitErr.Signal != "":
		return waitOutcome{result: res, err: &invoke.ExitError{Code: exitErr.Code, Signal: invoke.SIGTERM}}, true
	case p.d.clampExitCode && exitErr.Code >= signalBoundary:
		return waitOutcome{
			result: invoke.Result{ExitCode: -1, Duration: res.Duration},
			err:    &invoke.ExitError{Code: -1, Signal: invoke.SIGKILL},
		}, true
	case p.d.shell127AsNotFound && exitErr.Code == exitCommandNotFound:
		return waitOutcome{result: res, err: fmt.Errorf("misbehave: %w", invoke.ErrNotFound)}, true
	default:
		return waitOutcome{}, false
	}
}

// exitCommandNotFound is the shell's status for an unknown command.
const exitCommandNotFound = 127

func asExitError(err error) *invoke.ExitError {
	var exitErr *invoke.ExitError
	if errors.As(err, &exitErr) {
		return exitErr
	}

	return nil
}

// isTerminalStartError reports whether a Start failure is one that asking
// again will not change — the families the executor treats as terminal, so
// the launderTerminal defect knows which errors to disguise as retryable.
// Cancellation is deliberately excluded: a provider's own per-attempt
// deadline is a context error inside a genuine transport failure.
func isTerminalStartError(err error) bool {
	return errors.Is(err, invoke.ErrNotFound) ||
		errors.Is(err, invoke.ErrInvalidWorkdir) ||
		errors.Is(err, invoke.ErrClosed) ||
		errors.Is(err, invoke.ErrNotSupported)
}

// neverEOFReader blocks forever, simulating an inherited terminal stdin.
type neverEOFReader struct{}

func (neverEOFReader) Read(_ []byte) (int, error) {
	select {} // Block until the process side gives up.
}

// garbleWriter prepends garbage to the first write, corrupting any exact
// output expectation regardless of size.
type garbleWriter struct {
	w       io.Writer
	started bool
}

func (g *garbleWriter) Write(p []byte) (int, error) {
	if !g.started {
		g.started = true

		if _, err := g.w.Write([]byte("GARBLED:")); err != nil {
			return 0, err
		}
	}

	return g.w.Write(p)
}

// limitWriter forwards at most limit bytes and silently drops the rest.
type limitWriter struct {
	w       io.Writer
	limit   int
	written int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	remain := l.limit - l.written
	if remain <= 0 {
		return len(p), nil
	}

	if len(p) > remain {
		if _, err := l.w.Write(p[:remain]); err != nil {
			return 0, err
		}

		l.written = l.limit

		return len(p), nil
	}

	n, err := l.w.Write(p)
	l.written += n

	return n, err
}

// defectCase names one injected defect and the contract it must break.
type defectCase struct {
	name     string
	contract string
	defects  defects
}

// defectCatalog maps every contract to at least one defect that must make
// it fail. The matrix enforces full coverage.
func defectCatalog() []defectCase {
	return []defectCase{
		{name: "garbled output", contract: "core/captures-stdout", defects: defects{garbleOutput: true}},
		{name: "merged streams", contract: "core/streams-stay-separate", defects: defects{mergeStreams: true}},
		{name: "stdin never EOF", contract: "core/nil-stdin-is-eof", defects: defects{stdinNeverEOF: true}},
		{name: "swallowed stdin", contract: "core/stdin-is-delivered", defects: defects{swallowStdin: true}},
		{name: "truncated large output", contract: "core/large-output-is-complete", defects: defects{truncateOutput: true}},
		{name: "truncated large stdin", contract: "core/large-stdin-is-delivered", defects: defects{truncateStdin: true}},
		{name: "dropped env", contract: "core/env-overlays-base", defects: defects{dropEnv: true}},
		{name: "first-wins env", contract: "core/env-override-wins", defects: defects{reverseEnv: true}},
		{name: "ignored workdir", contract: "core/workdir-is-honored", defects: defects{ignoreWorkdir: true}},
		{name: "shell-joined argv", contract: "core/args-are-literal", defects: defects{shellJoinArgs: true}},
		{name: "exit-zero lie", contract: "core/exit-code-is-reported", defects: defects{exitZeroLie: true}},
		{name: "clamped exit code", contract: "core/exit-code-past-signal-boundary", defects: defects{clampExitCode: true}},
		{name: "zero duration", contract: "core/duration-is-measured", defects: defects{zeroDuration: true}},
		{name: "lying OS", contract: "core/os-matches-target", defects: defects{lieOS: true}},

		{name: "flip-flopping wait", contract: "lifecycle/wait-is-idempotent", defects: defects{waitFlipFlops: true}},
		{name: "flip-flopping concurrent wait", contract: "lifecycle/concurrent-wait-is-safe", defects: defects{waitFlipFlops: true}},
		{name: "ignored cancel", contract: "lifecycle/cancel-unblocks-wait", defects: defects{ignoreCancel: true}},
		{name: "cancel as exit error", contract: "lifecycle/cancel-unblocks-wait", defects: defects{exitErrorOnCancel: true}},
		{name: "deadline as cancel", contract: "lifecycle/deadline-unblocks-wait", defects: defects{hardcodeCanceled: true}},
		{name: "surviving process", contract: "lifecycle/cancel-terminates-process", defects: defects{ignoreCancel: true}},
		{name: "late cancel overwrites exit", contract: "lifecycle/cancel-after-exit-keeps-outcome", defects: defects{cancelOverwritesExit: true}},
		{name: "cancel during drain overwrites exit", contract: "lifecycle/cancel-during-drain-keeps-outcome", defects: defects{cancelOverwritesExit: true}},
		{name: "start despite cancel", contract: "lifecycle/start-on-canceled-context", defects: defects{ignoreCancel: true}},
		{name: "single process only", contract: "lifecycle/concurrent-processes-run", defects: defects{singleProcess: true}},
		{name: "close no-op", contract: "lifecycle/close-unblocks-wait", defects: defects{closeNoOp: true}},
		{name: "second close errors", contract: "lifecycle/close-is-idempotent", defects: defects{secondCloseErrors: true}},
		{name: "close forgets outcome", contract: "lifecycle/close-after-wait-keeps-outcome", defects: defects{closeForgets: true}},
		{name: "env close no-op", contract: "lifecycle/env-close-terminates-processes", defects: defects{envCloseNoOp: true}},
		{name: "silent signal", contract: "lifecycle/signal-terminates-process", defects: defects{silentSignal: true}},
		{name: "collapsed signal names", contract: "lifecycle/signal-attribution-round-trips", defects: defects{collapseSignalNames: true}},
		{name: "silent signal after exit", contract: "lifecycle/signal-after-exit-errors", defects: defects{silentSignal: true}},
		{name: "silent unsupported signal", contract: "lifecycle/unsupported-signal-normalized", defects: defects{silentSignal: true}},
		{name: "plain signal error", contract: "lifecycle/unsupported-signal-normalized", defects: defects{plainSignalError: true}},

		{name: "unclassified missing binary", contract: "errors/missing-binary-not-found", defects: defects{plainMissingBinary: true}},
		{name: "shell 127 as not-found", contract: "errors/shell-missing-binary-is-exit-127", defects: defects{shell127AsNotFound: true}},
		{name: "unclassified workdir", contract: "errors/bad-workdir-classified", defects: defects{plainWorkdirError: true}},
		{name: "unclassified lookpath", contract: "errors/lookpath-classifies", defects: defects{plainLookPathError: true}},
		{name: "bare lookpath name", contract: "errors/lookpath-classifies", defects: defects{bareLookPath: true}},
		{name: "env close no-op refusal", contract: "errors/closed-env-refuses-all", defects: defects{envCloseNoOp: true}},
		{name: "terminal laundered as transport", contract: "errors/terminal-outcomes-are-not-transport", defects: defects{launderTerminal: true}},

		{name: "corrupted uploads", contract: "transfer/roundtrip-preserves-content-and-mode", defects: defects{corruptUploads: true}},
		{name: "corrupted binary", contract: "transfer/binary-content-survives", defects: defects{corruptUploads: true}},
		{name: "flattened modes", contract: "transfer/roundtrip-preserves-content-and-mode", defects: defects{flattenModes: true}},
		{name: "dropped mode option", contract: "transfer/mode-override-applies-on-overwrite", defects: defects{dropModeOption: true}},
		{name: "destroy on failure", contract: "transfer/failure-preserves-destination", defects: defects{destroyOnFailure: true}},
		{name: "destroy on cancel", contract: "transfer/cancel-preserves-destination", defects: defects{destroyOnFailure: true}},
		{name: "destroy download on cancel", contract: "transfer/download-cancel-preserves-destination", defects: defects{destroyDownloadOnFailure: true}},
		{name: "shallow trees", contract: "transfer/tree-roundtrip-creates-parents", defects: defects{shallowTrees: true}},
		{name: "shallow empty tree", contract: "transfer/empty-files-and-dirs", defects: defects{shallowTrees: true}},
		{name: "dropped symlinks", contract: "transfer/symlinks-preserve", defects: defects{dropSymlinks: true}},
		{name: "follow lies on escape", contract: "transfer/follow-rejects-escapes", defects: defects{followLies: true}},
		{name: "follow lies on content", contract: "transfer/symlink-follow-copies-content", defects: defects{followLies: true}},
		{name: "forced special skip", contract: "transfer/special-files-error-by-default", defects: defects{alwaysSkipSpecial: true}},
		{name: "zeroed progress totals", contract: "transfer/progress-reports-totals", defects: defects{zeroProgressTotals: true}},
		{name: "transfer ignores cancel", contract: "transfer/canceled-before-start-does-nothing", defects: defects{transferIgnoresCancel: true}},

		{name: "pretend TTY", contract: "tty/allocates-terminal", defects: defects{claimTTY: true}},
		{name: "silently stripped TTY", contract: "tty/unsupported-errors", defects: defects{stripTTY: true}},
	}
}

// newReferenceFactory returns a Factory over the clean local provider plus
// a cleanup that closes the underlying environments even when a defect
// makes the wrapper's own lifecycle misbehave.
func newReferenceFactory(d defects) (Factory, func()) {
	var bases []invoke.Environment

	factory := func(t T) invoke.Environment {
		env, err := local.New()
		require.NoError(t, err, "constructing local reference environment")

		bases = append(bases, env)

		return &misbehaveEnv{base: env, d: d}
	}

	cleanup := func() {
		for _, env := range bases {
			_ = env.Close()
		}
	}

	return factory, cleanup
}
