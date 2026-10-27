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
// streams, cd, exit, and exec.
//
// A script reaching outside that subset is refused by name rather than
// run wrongly. Pipelines, || lists, redirection to a file, input
// redirection, backquotes, newline-separated commands, background
// commands, comments, globs, backslash escapes, arithmetic expansion,
// the ${ operator forms, and the special and positional parameters ($?,
// $1, $$) are all refused. Start refuses such a script before any
// process exists, with an error wrapping [invoke.ErrNotSupported]; a
// script it cannot see into, such as one nested in quotes as
// sh -c 'a | b', is refused when it runs, with the construct named on
// standard error and exit status 93. The refusal is the point: an
// unrecognized character is just another character to a tokenizer, so
// an unsupported construct would otherwise be run as something else —
// `false || echo rescued` exiting 1 having printed nothing, where every
// real target exits 0 having printed — and a test asserting that would
// assert the opposite of the truth.
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
// Inside scripts, cd (to $HOME with no argument), exit (numeric
// statuses only) and exec COMMAND (which runs COMMAND and ends the
// script with its status) are the shell's own. Anything else — ls, cp,
// grep, env, chmod — is an unknown command: [invoke.ErrNotFound] from
// Start, the shell's exit 127 from a script. Register your own with
// [Environment.Handle], which overrides any builtin of the same name
// and is reachable everywhere the name can be written. A script needing
// more than the subset belongs in a handler, or on a real target.
//
// The builtins, and handlers registered under a bare name, live in
// /usr/bin, which is where LookPath reports them. Start finds a bare
// name only when the PATH the command runs with lists /usr/bin: a PATH
// in Command.Env, or the base environment's. A handler registered under
// a path lives at that path, and is reached by it whatever PATH says. A
// script's own changes to PATH are not followed.
//
// # The filesystem
//
// One store holds the target's files, directories and symbolic links,
// and everything that touches the target goes through it: the builtins
// mutate it directly, Upload and Download reach it through the shared
// transfer engine the real providers use, and [Environment.FS] reads it
// as a live [io/fs.FS] for assertions. A file an upload wrote is the
// file test -f finds and the one fs.ReadFile returns.
//
// # What the fake does not simulate
//
// TTY allocation is refused with [invoke.ErrNotSupported], as
// [Environment.Capabilities] reports. Files carry permission bits but no
// owner, and no modification time: ModTime is always the zero time, so
// an assertion on mtimes needs a real target.
package fake

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/internal/lifetime"
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

	// Dir is the working directory the command runs in: Command.Dir,
	// or / when that is empty, from Start; the shell's current
	// directory from a script.
	Dir string

	// Env is the resolved environment: the fake's base environment with
	// the command's overlay applied, in "KEY=VALUE" form.
	Env []string
}

// Handler simulates one command: it reads and writes the session streams
// and returns the exit code. Honoring ctx cancellation keeps a scripted
// command responsive to the lifecycle machinery, which handles the
// resulting classification (cancellation, Close, signals) itself.
//
// cmd is the invocation as written. From Start it is the caller's
// Command unchanged, its Dir possibly empty and its Env only the
// overlay the caller set; from a script it carries the Path and Args the
// script wrote, the shell's current directory as Dir, and no Env. The
// state to act on is the [Session]'s: on both routes Session.Dir is the
// directory the command runs in and Session.Env the full environment.
type Handler func(ctx context.Context, cmd invoke.Command, s *Session) int

// Environment is a simulated execution target.
type Environment struct {
	// mu guards handlers, calls and baseEnv.
	mu       sync.Mutex
	handlers map[string]Handler
	calls    []invoke.Command
	baseEnv  map[string]string

	fs *vfs

	// life is what Close ends: the simulated processes still running and
	// the transfers in flight.
	life *lifetime.Lifetime
}

var _ invoke.Environment = (*Environment)(nil)

// New returns a fresh simulated target with an empty /tmp, a small base
// environment, and no recorded calls.
func New(opts ...Option) *Environment {
	e := &Environment{
		handlers: make(map[string]Handler),
		fs:       newVFS(),
		life:     lifetime.New(),
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
	if err := e.life.Err(); err != nil {
		return nil, fmt.Errorf("fake: start: %w", err)
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
	if (handler == nil && !builtin) || !e.onPath(cmd) {
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

	s, guard := e.newSession(cmd, stdio)

	proc, err := e.spawn(ctx, cmd, guard, func(runCtx context.Context) (int, bool) {
		if handler != nil {
			return e.runHandler(runCtx, handler, cmd, s)
		}

		return dispatch(runCtx, s, cmd.Path, cmd.Args)
	})
	if err != nil {
		return nil, err
	}

	return proc, nil
}

// LookPath resolves name against the fake's handlers and builtins. The
// answer is itself resolvable: what LookPath reports, Start accepts.
func (e *Environment) LookPath(ctx context.Context, name string) (string, error) {
	if err := e.life.Err(); err != nil {
		return "", fmt.Errorf("fake: lookpath: %w", err)
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

// Close stops any transfer in flight and terminates any simulated process
// still running, waiting for both. It is idempotent.
func (e *Environment) Close() error {
	e.life.Close()

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

// commandHome is where the builtins and bare-named handlers live, as
// LookPath reports them.
const commandHome = "/usr/bin"

// onPath reports whether a bare command name is reachable through the
// PATH the command runs with: the caller's, when Command.Env sets one,
// or the base environment's. Every bare-named command lives in
// commandHome, so a PATH that does not list it finds none of them, as on
// a real target. A name with a slash is not looked up.
func (e *Environment) onPath(cmd invoke.Command) bool {
	if strings.Contains(cmd.Path, "/") {
		return true
	}

	pathList, ok := cmd.LookupEnv("PATH")
	if !ok {
		e.mu.Lock()
		pathList = e.baseEnv["PATH"]
		e.mu.Unlock()
	}

	for dir := range strings.SplitSeq(pathList, ":") {
		if path.Clean(dir) == commandHome {
			return true
		}
	}

	return false
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

// record notes a command that is about to run. It is called before the
// body runs, so a handler reading Calls finds its own invocation, and one
// that starts another command is recorded before it.
func (e *Environment) record(cmd invoke.Command) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.calls = append(e.calls, cmd)
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
