// Package fake is a scriptable, contract-verified test double for
// [invoke.Environment].
//
// The fake simulates a small POSIX-ish target: a virtual filesystem, a
// minimal shell and builtin vocabulary, and full transfer semantics
// between the real host filesystem and the virtual one. It passes the
// same invoketest contract suite the real providers pass, so tests
// written against it exercise contract-accurate lifecycle, error, and
// stream behavior — not a mock that can be programmed into impossible
// states.
//
// Consumer-registered handlers take precedence over the builtin
// vocabulary; commands that are neither registered nor builtin fail with
// [invoke.ErrNotFound], exactly as a missing binary does on a real
// target.
//
// # What the shell runs
//
// [invoke.Shell] scripts are interpreted, not executed, and the
// interpreter covers a subset: sequencing with ; and &&, single and
// double quotes, $NAME and ${NAME} expansion, $(command) substitution,
// redirection to /dev/null (flush or spaced) and between the two output
// streams, cd, and exit.
//
// A script reaching outside that subset is refused — wrapping
// [invoke.ErrNotSupported], before any process exists — rather than run
// wrongly. Pipelines, || lists, redirection to a file, input redirection,
// backquotes, newline-separated commands, background commands, comments,
// globs, backslash escapes, arithmetic expansion, the ${ operator forms,
// and the special and positional parameters ($?, $1, $$) are all refused
// by name. The alternative was worse than useless: an unrecognized
// character is just another character to a tokenizer, so a pipeline used
// to become arguments to the first command and `false || echo rescued`
// exited 1 having printed nothing, where every real target exits 0
// having printed. A test asserting that is not merely unverified — it
// asserts the opposite of the truth.
//
// # The builtin vocabulary
//
// The builtins are a vocabulary rather than an implementation: they
// cover the forms the contract suite and ordinary shell-outs use, and a
// form outside them — a test operator beyond the set below, most
// utility flags — fails loudly on standard error rather than being
// answered falsely. What the fake answers without a handler:
//
//   - sh -c, running the script through this same interpreter
//   - echo (no flags) and printf (%s and %% conversions, the common
//     backslash escapes)
//   - cat, reading standard input; file arguments are ignored
//   - test, with the one-argument form, an optional leading !, and the
//     unary -e, -d, -f, -L, -n, -z, and -t; test and cd do not follow
//     symbolic links
//   - true, false, sleep SECONDS, pwd, and uname (which reports Linux,
//     matching [Environment.OS])
//   - find PATH -maxdepth 0 -perm MODE, and dd if=/dev/zero with
//     optional bs= and count=
//   - mkdir [-p], touch, and rm [-r|-f|-rf], each with the failure
//     modes a real target has
//
// Inside scripts, cd (to $HOME with no argument) and exit (numeric
// statuses only) are the shell's own. Anything else — ls, cp, grep,
// env, chmod — is an unknown command: [invoke.ErrNotFound] from Start,
// the shell's exit 127 from a script. Register your own with
// [Environment.Handle], which overrides any builtin of the same name
// and is reachable everywhere the name can be written. A script needing
// more than the subset belongs in a handler, or on a real target.
package fake

import (
	"context"
	"fmt"
	"io"
	"path"
	"sync"

	"github.com/ruffel/invoke"
)

// Session is the execution state a [Handler] runs with.
type Session struct {
	// Stdin is the invocation's standard input (never nil; empty when
	// the caller wired none).
	Stdin io.Reader

	// Stdout and Stderr are the invocation's output streams (never
	// nil; discarding when the caller wired none).
	Stdout io.Writer
	Stderr io.Writer

	// Dir is the working directory the command was started with.
	Dir string

	// Env is the resolved environment: the fake's base environment with
	// the command's overlay applied, in "KEY=VALUE" form.
	Env []string
}

// Handler simulates one command: it reads and writes the session streams
// and returns the exit code. Honoring ctx cancellation keeps a scripted
// command responsive to the lifecycle machinery, which handles the
// resulting classification (cancellation, Close, signals) itself.
type Handler func(ctx context.Context, cmd invoke.Command, s *Session) int

// Environment is a simulated execution target.
type Environment struct {
	mu       sync.Mutex
	closed   bool
	handlers map[string]Handler
	calls    []invoke.Command
	active   map[*process]struct{}

	fs      *vfs
	baseEnv map[string]string
}

var _ invoke.Environment = (*Environment)(nil)

// Option configures the fake at construction.
type Option func(*Environment)

// WithEnv adds KEY=VALUE pairs to the fake target's base environment.
func WithEnv(pairs ...string) Option {
	return func(e *Environment) {
		for _, pair := range pairs {
			if key, value, ok := cutPair(pair); ok {
				e.baseEnv[key] = value
			}
		}
	}
}

// New returns a fresh simulated target with an empty /tmp, a small base
// environment, and no recorded calls.
func New(opts ...Option) *Environment {
	e := &Environment{
		handlers: make(map[string]Handler),
		active:   make(map[*process]struct{}),
		fs:       newVFS(),
		baseEnv: map[string]string{
			"PATH": "/usr/local/bin:/usr/bin:/bin",
			"HOME": "/root",
		},
	}

	for _, opt := range opts {
		opt(e)
	}

	return e
}

// Handle registers a handler for commands whose Path equals name,
// overriding any builtin of the same name. A handler is reachable
// everywhere the name can be written: started directly, invoked from a
// [invoke.Shell] script or a $(...) substitution, and through the path
// LookPath reports for it. Calls records commands given to Start; a
// script's internal invocations belong to the script.
func (e *Environment) Handle(name string, h Handler) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.handlers[name] = h
}

// Calls returns the commands started so far, in order. Probe and cleanup
// commands issued by test helpers appear too: the record is honest.
func (e *Environment) Calls() []invoke.Command {
	e.mu.Lock()
	defer e.mu.Unlock()

	return append([]invoke.Command(nil), e.calls...)
}

// Start launches a simulated process for cmd.
func (e *Environment) Start(ctx context.Context, cmd invoke.Command, stdio invoke.IO) (invoke.Process, error) {
	if err := e.checkOpen("start"); err != nil {
		return nil, err
	}

	if err := cmd.Validate(); err != nil {
		return nil, fmt.Errorf("fake: start: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("fake: start: %w", err)
	}

	if stdio.TTY != nil {
		return nil, fmt.Errorf("fake: start: tty allocation: %w", invoke.ErrNotSupported)
	}

	if cmd.Dir != "" && !e.fs.isDir(vfsClean("/", cmd.Dir)) {
		return nil, fmt.Errorf("fake: start: workdir %q: %w", cmd.Dir, invoke.ErrInvalidWorkdir)
	}

	handler, builtin := e.resolveCommand(cmd.Path)
	if handler == nil && !builtin {
		return nil, fmt.Errorf("fake: start %q: %w", cmd.Path, invoke.ErrNotFound)
	}

	// Refused before the process exists, so it cannot be mistaken for the
	// command running and failing. A script the shell cannot run is a
	// thing this target cannot do, not a verdict about the script.
	if handler == nil {
		if err := shellScriptSupported(cmd); err != nil {
			return nil, err
		}
	}

	e.record(cmd)

	s, guard := e.newSession(cmd, stdio)

	return e.spawn(ctx, guard, func(runCtx context.Context) (int, bool) {
		if handler != nil {
			return e.runHandler(runCtx, handler, cmd, s)
		}

		return dispatch(runCtx, s, cmd.Path, cmd.Args)
	}), nil
}

// LookPath resolves name against the fake's handlers and builtins. The
// answer is itself resolvable: what LookPath reports, Start accepts.
func (e *Environment) LookPath(ctx context.Context, name string) (string, error) {
	if err := e.checkOpen("lookpath"); err != nil {
		return "", err
	}

	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("fake: lookpath: %w", err)
	}

	if handler, builtin := e.resolveCommand(name); handler != nil || builtin {
		if path.IsAbs(name) {
			return name, nil
		}

		return "/usr/bin/" + name, nil
	}

	return "", fmt.Errorf("fake: lookpath %q: %w", name, invoke.ErrNotFound)
}

// OS reports the simulated target's operating system.
func (e *Environment) OS() invoke.TargetOS {
	return invoke.OSLinux
}

// Capabilities reports the simulated target's features: signal delivery
// and symlink-preserving transfers work; TTY allocation is not simulated.
func (e *Environment) Capabilities() invoke.Capabilities {
	return invoke.Capabilities{
		TTY:             false,
		Signals:         true,
		SymlinkPreserve: true,
	}
}

// Close marks the fake closed and terminates simulated processes still
// running. It is idempotent.
func (e *Environment) Close() error {
	e.mu.Lock()

	if e.closed {
		e.mu.Unlock()

		return nil
	}

	e.closed = true

	procs := make([]*process, 0, len(e.active))
	for p := range e.active {
		procs = append(procs, p)
	}

	e.mu.Unlock()

	for _, p := range procs {
		_ = p.Close()
	}

	return nil
}

func (e *Environment) runHandler(ctx context.Context, h Handler, cmd invoke.Command, s *session) (int, bool) {
	view := &Session{
		Stdin:  s.stdin,
		Stdout: s.stdout,
		Stderr: s.stderr,
		Dir:    s.dir,
		Env:    flattenEnv(s.env),
	}

	code := h(ctx, cmd, view)

	// Cancellation is authoritative for a simulated process: the fake
	// only cancels when the lifecycle machinery (context, Close, or a
	// signal) terminated the command.
	return code, ctx.Err() != nil
}

func (e *Environment) resolve(name string) (Handler, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if h, ok := e.handlers[name]; ok {
		return h, false
	}

	return nil, builtinKnown(name)
}

// resolveCommand resolves an invoked path the way the target's own
// lookup would: the exact registered or builtin name, or the basename
// of the conventional /usr/bin home LookPath reports for every known
// name. One rule serves Start, LookPath and the shell, so the three
// cannot disagree about what exists.
func (e *Environment) resolveCommand(pathStr string) (Handler, bool) {
	if h, b := e.resolve(pathStr); h != nil || b {
		return h, b
	}

	if alias := commandName(pathStr); alias != pathStr {
		return e.resolve(alias)
	}

	return nil, false
}

// commandName maps an invoked path to the name the fake knows it by:
// bare names as written, and the basename of the conventional /usr/bin
// home.
func commandName(pathStr string) string {
	if dir, base := path.Split(pathStr); dir == "/usr/bin/" {
		return base
	}

	return pathStr
}

func (e *Environment) record(cmd invoke.Command) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.calls = append(e.calls, cmd)
}

func (e *Environment) checkOpen(op string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.closed {
		return fmt.Errorf("fake: %s: %w", op, invoke.ErrClosed)
	}

	return nil
}

func (e *Environment) track(p *process) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.active[p] = struct{}{}
}

func (e *Environment) untrack(p *process) {
	e.mu.Lock()
	defer e.mu.Unlock()

	delete(e.active, p)
}

func cutPair(pair string) (string, string, bool) {
	for i := range len(pair) {
		if pair[i] == '=' {
			return pair[:i], pair[i+1:], true
		}
	}

	return "", "", false
}

func flattenEnv(env map[string]string) []string {
	flat := make([]string, 0, len(env))
	for key, value := range env {
		flat = append(flat, key+"="+value)
	}

	return flat
}
