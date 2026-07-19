// Package docker executes commands and transfers files inside a running
// container. It implements [invoke.Environment], verified against the
// invoketest contract suite.
//
// The daemon is found the way the docker command finds it: an endpoint
// passed to [WithHost], then DOCKER_HOST, then the endpoint recorded by
// the current context — which is where installations that do not use the
// conventional socket, such as Colima and Rancher Desktop, put theirs.
//
// Commands are delivered to the daemon as a real argument vector, so
// nothing is reinterpreted by a shell, and environment variables travel
// over the API rather than on a command line. Signal delivery needs a
// shell in the container to record the in-container process id; a
// container without one reports the capability as unavailable rather than
// accepting signals and dropping them.
package docker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/client"
	"github.com/ruffel/invoke"
)

// Environment is a connection to a running container.
type Environment struct {
	cfg    *Config
	client client.APIClient
	id     string
	os     invoke.TargetOS

	// hasShell records whether the container has a shell, which signal
	// delivery depends on.
	hasShell bool

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

	opts := []client.Opt{client.FromEnv, client.WithAPIVersionNegotiation()}
	if host != "" {
		opts = append(opts, client.WithHost(host))
	}

	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, &invoke.TransportError{Op: "connect", Err: err}
	}

	env := &Environment{
		cfg:    cfg,
		client: cli,
		active: make(map[*process]struct{}),
	}

	if err := env.inspectContainer(ctx); err != nil {
		_ = cli.Close()

		return nil, err
	}

	env.os = env.detectOS(ctx)
	env.hasShell = env.detectShell(ctx)

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

	out, code, err := e.runRaw(ctx, []string{"sh", "-c", `command -v "$1"`, "sh", name})
	if err != nil {
		return "", fmt.Errorf("docker: lookpath %q: %w", name, err)
	}

	if code != 0 {
		return "", fmt.Errorf("docker: lookpath %q: %w", name, invoke.ErrNotFound)
	}

	return strings.TrimSpace(out), nil
}

// Close releases the daemon connection, terminating commands still
// running.
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

	return e.client.Close()
}

// inspectContainer verifies the target exists and is running, so a
// mistyped name fails at construction rather than on first use.
func (e *Environment) inspectContainer(ctx context.Context) error {
	probeCtx, cancel := context.WithTimeout(ctx, e.cfg.timeout())
	defer cancel()

	info, err := e.client.ContainerInspect(probeCtx, e.cfg.Container)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return fmt.Errorf("docker: container %q: %w", e.cfg.Container, invoke.ErrNotFound)
		}

		return &invoke.TransportError{Op: "inspect", Err: err}
	}

	if info.State == nil || !info.State.Running {
		return fmt.Errorf("docker: container %q is not running", e.cfg.Container)
	}

	e.id = info.ID

	return nil
}

// detectOS classifies the container's operating system by asking it,
// rather than trusting the daemon's platform label, so the answer matches
// what commands actually run against.
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

// detectShell reports whether the container has a shell, which signal
// delivery and path lookup depend on.
func (e *Environment) detectShell(ctx context.Context) bool {
	probeCtx, cancel := context.WithTimeout(ctx, e.cfg.timeout())
	defer cancel()

	_, code, err := e.runRaw(probeCtx, []string{"sh", "-c", "exit 0"})

	return err == nil && code == 0
}

func (e *Environment) checkOpen(op string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.closed {
		return fmt.Errorf("docker: %s: %w", op, invoke.ErrClosed)
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
