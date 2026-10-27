// Package docker executes commands and transfers files inside a running
// container. It implements [invoke.Environment], verified against the
// invoketest contract suite.
//
// The daemon is found the way the docker command finds it: an endpoint
// passed to [WithHost], then a context named by [WithContext] or
// DOCKER_CONTEXT, then DOCKER_HOST, then the endpoint recorded by the
// current context — which is where installations that do not use the
// conventional socket, such as Colima and Rancher Desktop, put theirs.
//
// The daemon must serve Engine API version 1.40 or newer, which Docker
// 19.03 was the first to do. That is the oldest version Docker's client
// library speaks, and an older daemon is refused when the Environment is
// created — as is a DOCKER_API_VERSION naming an older version.
//
// Commands are delivered to the daemon as a real argument vector, so
// nothing is reinterpreted by a shell, and environment variables travel
// over the API rather than on a command line. Signal delivery needs a
// shell in the container to record the in-container process id; a
// container without one reports the capability as unavailable rather than
// accepting signals and dropping them. Without a shell, Close and
// cancellation cannot stop a command either: they end its input and let
// it go, returning at once with an outcome that says it was abandoned,
// while the command runs on in the container until it ends. Path lookup,
// uploads and the check Start makes before running a command need the
// shell too; downloads do not. Without that check, a missing executable
// or working directory is reported the way the daemon reports it, as exit
// status 127.
package docker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
	"github.com/moby/moby/client/pkg/versions"
	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/internal/lifetime"
)

// Environment is a connection to a running container.
type Environment struct {
	cfg    *Config
	client client.APIClient
	id     string
	os     invoke.TargetOS

	// hasShell records whether the container has a shell, and shell is
	// where it lives. Without one, Signal, LookPath and Upload fail with
	// invoke.ErrNotSupported, Start skips its pre-flight check, and Close
	// and cancellation abandon a command rather than end it. Download and
	// running a command need no shell.
	hasShell bool
	shell    string

	// life is what Close ends, taking in-flight transfers with it.
	life *lifetime.Lifetime

	// mu guards closed and active: the processes Close terminates, kept
	// as the core providers keep theirs in internal/lifetime, which this
	// module adopts once a release carries it.
	mu     sync.Mutex
	closed bool
	active map[*process]struct{}
}

var _ invoke.Environment = (*Environment)(nil)

// New connects to the daemon and returns an Environment for the named
// container, which must already be running.
//
// ctx bounds establishing the connection only — which is several round
// trips to the daemon, not one. It does not govern the Environment
// afterwards, which lives until Close.
func New(ctx context.Context, container string, opts ...Option) (*Environment, error) {
	cfg := &Config{Container: container}
	for _, opt := range opts {
		opt(cfg)
	}

	return NewFromConfig(ctx, cfg)
}

// NewFromConfig connects using a Config assembled directly. ctx bounds
// establishing the connection, as in [New].
func NewFromConfig(ctx context.Context, cfg *Config) (*Environment, error) {
	if strings.TrimSpace(cfg.Container) == "" {
		return nil, errors.New("docker: container is required")
	}

	host, err := resolveHost(cfg)
	if err != nil {
		return nil, err
	}

	opts := []client.Opt{client.FromEnv}
	if host != "" {
		opts = append(opts, client.WithHost(host))
	}

	cli, err := client.New(opts...)
	if err != nil {
		return nil, &invoke.TransportError{Op: "connect", Err: err}
	}

	if err := checkAPIVersion(cli); err != nil {
		_ = cli.Close()

		return nil, err
	}

	env := &Environment{
		cfg:    cfg,
		client: cli,
		active: make(map[*process]struct{}),
		life:   lifetime.New(),
	}

	if err := env.inspectContainer(ctx); err != nil {
		_ = cli.Close()

		return nil, err
	}

	env.os = env.detectOS(ctx)
	env.shell, env.hasShell = env.detectShell(ctx)

	return env, nil
}

// OS reports the container's operating system, detected once at connect
// time.
func (e *Environment) OS() invoke.TargetOS {
	return e.os
}

// Capabilities reports the container's optional features.
//
// Signal delivery requires a shell to record the in-container process id;
// without one the capability is not declared, so signal requests fail
// rather than being silently dropped.
//
// Terminal allocation is available: the daemon's exec API takes a
// terminal flag and an initial size.
func (e *Environment) Capabilities() invoke.Capabilities {
	return invoke.Capabilities{
		TTY:             true,
		Signals:         e.hasShell,
		SymlinkPreserve: true,
	}
}

// LookPath resolves name inside the container.
func (e *Environment) LookPath(ctx context.Context, name string) (string, error) {
	if err := e.checkOpen("lookpath"); err != nil {
		return "", err
	}

	if !e.hasShell {
		return "", fmt.Errorf("docker: lookpath %q: container has no shell: %w", name, invoke.ErrNotSupported)
	}

	out, code, err := e.runRaw(ctx, []string{"sh", "-c", lookPathScript(), "sh", name})
	if err != nil {
		return "", fmt.Errorf("docker: lookpath %q: %w", name, err)
	}

	if code != 0 {
		return "", fmt.Errorf("docker: lookpath %q: %w", name, invoke.ErrNotFound)
	}

	return strings.TrimSpace(out), nil
}

// Close marks the environment closed, stops transfers in flight and
// waits for them, then terminates commands still running and waits for
// each outcome to settle. It is idempotent.
//
// A command in a container without a shell cannot be terminated: Close
// lets it go instead, its Wait returns at once saying it was abandoned,
// and it runs on in the container until it ends.
//
// The daemon client closes only its idle connections, so a call Close
// does not own and that is already under way, such as a LookPath, runs
// to its end; one begun afterwards fails with invoke.ErrClosed.
func (e *Environment) Close() error {
	e.mu.Lock()

	if e.closed {
		e.mu.Unlock()

		// A Close racing another still waits for the transfers the first
		// one stopped: returning while they unwind would let them run
		// on past it.
		e.life.Close()

		return nil
	}

	e.closed = true

	procs := make([]*process, 0, len(e.active))
	for p := range e.active {
		procs = append(procs, p)
	}

	e.mu.Unlock()

	e.life.Close()

	for _, p := range procs {
		_ = p.Close()
	}

	return e.client.Close()
}

// inspectContainer verifies the target exists and is running, so a
// mistyped name fails at construction rather than on first use.
func (e *Environment) inspectContainer(ctx context.Context) error {
	probeCtx, cancel := context.WithTimeout(ctx, e.cfg.timeout())
	defer cancel()

	info, err := e.client.ContainerInspect(probeCtx, e.cfg.Container, client.ContainerInspectOptions{})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return fmt.Errorf("docker: container %q: %w", e.cfg.Container, invoke.ErrNotFound)
		}

		return &invoke.TransportError{Op: "inspect", Err: err}
	}

	if info.Container.State == nil || !info.Container.State.Running {
		return fmt.Errorf("docker: container %q is not running", e.cfg.Container)
	}

	e.id = info.Container.ID

	return nil
}

// checkAPIVersion refuses an API version below the oldest this provider
// supports.
//
// Only DOCKER_API_VERSION can set one. The client uses a version set that
// way as given — it neither negotiates it nor checks it against its own
// floor — so a version below the floor would carry the provider to a
// daemon it does not support, and the mismatch would surface part-way
// through a command rather than here. Without the variable the client
// starts from its newest version and negotiates down no further than the
// floor, which this never refuses.
func checkAPIVersion(cli *client.Client) error {
	version := cli.ClientVersion()
	if !versions.LessThan(version, client.MinAPIVersion) {
		return nil
	}

	return fmt.Errorf("docker: API version %s, set by %s, is older than the oldest this provider supports (%s): %w",
		version, client.EnvOverrideAPIVersion, client.MinAPIVersion, invoke.ErrNotSupported)
}

// detectOS classifies the container's operating system by asking it,
// rather than trusting the daemon's platform label, so the answer matches
// what commands actually run against.
//
// A container that cannot answer, such as an image without uname, and an
// answer it does not recognize are both taken as Linux: the provider
// supports Linux containers, as the README's stability section says.
// The ssh provider, whose hosts vary, reports invoke.OSUnknown instead.
func (e *Environment) detectOS(ctx context.Context) invoke.TargetOS {
	probeCtx, cancel := context.WithTimeout(ctx, e.cfg.timeout())
	defer cancel()

	out, code, err := e.runRaw(probeCtx, []string{"uname", "-s"})
	if err != nil || code != 0 {
		return invoke.OSLinux
	}

	switch strings.TrimSpace(out) {
	case "Darwin":
		return invoke.OSDarwin
	case "Linux":
		return invoke.OSLinux
	default:
		return invoke.OSLinux
	}
}

// detectShell reports where the container's shell is, and whether it has
// one; the hasShell field lists what depends on it.
//
// The path is found under the container's own PATH, once, because the
// wrapper every command runs under is started with the caller's
// environment, and so with the caller's PATH. A PATH naming only the
// caller's own tools has no sh on it, and the wrapper could not start.
// A shell that ran but cannot be found on its own PATH is still a shell,
// and is named as it was found.
func (e *Environment) detectShell(ctx context.Context) (string, bool) {
	probeCtx, cancel := context.WithTimeout(ctx, e.cfg.timeout())
	defer cancel()

	out, code, err := e.runRaw(probeCtx, []string{"sh", "-c", shellProbeScript(), "sh", "sh"})
	if err != nil || code != 0 {
		return "", false
	}

	if found := strings.TrimSpace(out); found != "" {
		return found, true
	}

	return "sh", true
}

func (e *Environment) checkOpen(op string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.closed {
		return fmt.Errorf("docker: %s: %w", op, invoke.ErrClosed)
	}

	return nil
}

// track registers a running process so Close can terminate it, unless the
// environment has closed in the meantime.
//
// Start checks the closed flag once at entry and then spends several
// daemon round-trips creating the exec. A Close landing in that window has
// already snapshotted the active set and moved on, so a process added
// afterwards would run in a closed environment with nothing left to stop
// it. Re-checking here under the same lock closes that gap: a process is
// either registered before Close snapshots, and terminated with the rest,
// or refused, and torn down by its caller.
func (e *Environment) track(p *process) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.closed {
		return fmt.Errorf("docker: start: %w", invoke.ErrClosed)
	}

	e.active[p] = struct{}{}

	return nil
}

func (e *Environment) untrack(p *process) {
	e.mu.Lock()
	defer e.mu.Unlock()

	delete(e.active, p)
}
