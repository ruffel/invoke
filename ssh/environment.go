// Package ssh executes commands and transfers files on a remote host over
// SSH. It implements [invoke.Environment], verified against the invoketest
// contract suite.
//
// Commands are delivered to the remote login shell as a single, shell-safe
// command line (the SSH protocol carries a command string, not an argv),
// with environment variables sent out of band so they do not appear in the
// remote process table. Host-key verification is fail-closed: a connection
// requires known_hosts, an explicit callback, or an explicit insecure
// override.
package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ruffel/invoke"
	"golang.org/x/crypto/ssh"
)

// Environment is a connection to a remote host over SSH.
type Environment struct {
	cfg    *Config
	client *ssh.Client
	os     invoke.TargetOS

	// agentConn is the SSH agent socket, held open for the life of the
	// connection because agent authentication signs on demand.
	agentConn io.Closer

	// stopKeepAlive ends the keepalive loop, and keepAliveDone closes once
	// it has actually stopped, so Close never outlives its own goroutine.
	stopKeepAlive context.CancelFunc
	keepAliveDone chan struct{}

	mu     sync.Mutex
	closed bool
	active map[*process]struct{}
}

var _ invoke.Environment = (*Environment)(nil)

// New connects to host over SSH and returns an Environment for it.
//
// ctx bounds establishing the connection only. It does not govern the
// Environment afterwards, which lives until Close.
func New(ctx context.Context, host string, opts ...Option) (*Environment, error) {
	cfg := &Config{Host: host}
	for _, opt := range opts {
		opt(cfg)
	}

	return NewFromConfig(ctx, cfg)
}

// NewFromConfig connects using a Config assembled directly. ctx bounds
// establishing the connection, as in [New].
func NewFromConfig(ctx context.Context, cfg *Config) (*Environment, error) {
	if strings.TrimSpace(cfg.Host) == "" {
		return nil, errors.New("ssh: host is required")
	}

	client, agentConn, err := connect(ctx, cfg)
	if err != nil {
		return nil, err
	}

	env := &Environment{
		cfg:       cfg,
		client:    client,
		agentConn: agentConn,
		active:    make(map[*process]struct{}),
	}

	env.os = env.detectOS(ctx)

	// Deliberately not the caller's context: the probe loop belongs to
	// the connection, which outlives whatever was being done when it was
	// opened. Ending it with that work would leave the connection
	// unwatched for the rest of its life.
	//nolint:contextcheck // The loop's lifetime is the connection's; Close ends it.
	env.startKeepAlive()

	return env, nil
}

// connect establishes the SSH client connection, bounding both the TCP
// dial and the handshake by the configured timeout.
func connect(ctx context.Context, cfg *Config) (*ssh.Client, io.Closer, error) {
	auth, agentConn, err := authMethods(cfg)
	if err != nil {
		return nil, nil, err
	}

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.port()))

	hostKeyCB, algorithms, err := resolveHostKey(cfg, addr)
	if err != nil {
		closeAgent(agentConn)

		return nil, nil, err
	}

	clientCfg := &ssh.ClientConfig{
		User:              loginUser(cfg.User),
		Auth:              auth,
		HostKeyCallback:   hostKeyCB,
		HostKeyAlgorithms: algorithms,
		Timeout:           cfg.timeout(),
	}

	// The configured timeout is an upper bound; the caller's context can
	// cut setup shorter, and does when it carries the earlier deadline.
	dialCtx, cancel := context.WithTimeout(ctx, cfg.timeout())
	defer cancel()

	var dialer net.Dialer

	conn, err := dialer.DialContext(dialCtx, "tcp", addr)
	if err != nil {
		closeAgent(agentConn)

		return nil, nil, &invoke.TransportError{Op: "dial", Err: err}
	}

	// Bound the handshake too: the dial context does not reach it, and a
	// server that accepts and then says nothing would otherwise hold the
	// call open indefinitely.
	_ = conn.SetDeadline(handshakeDeadline(dialCtx, cfg.timeout()))

	// The deadline covers timeouts; a plain cancellation has no deadline
	// to fire, so a watcher closes the socket — which is what unblocks a
	// handshake in progress.
	handshakeDone := make(chan struct{})

	go func() {
		select {
		case <-dialCtx.Done():
			_ = conn.Close()
		case <-handshakeDone:
		}
	}()

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, clientCfg)

	close(handshakeDone)

	if err != nil {
		_ = conn.Close()

		closeAgent(agentConn)

		// When the caller's own context ended the handshake, that is
		// the cause worth reporting.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, fmt.Errorf("ssh: connect: %w", ctxErr)
		}

		return nil, nil, &invoke.TransportError{Op: "handshake", Err: err}
	}

	_ = conn.SetDeadline(time.Time{})

	return ssh.NewClient(sshConn, chans, reqs), agentConn, nil
}

// handshakeDeadline is the earlier of the context's deadline and the
// configured timeout, so neither bound is exceeded.
func handshakeDeadline(ctx context.Context, timeout time.Duration) time.Time {
	deadline := time.Now().Add(timeout)

	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		return ctxDeadline
	}

	return deadline
}

// loginUser returns the configured user or the current OS user.
func loginUser(configured string) string {
	if configured != "" {
		return configured
	}

	if u, err := user.Current(); err == nil {
		return u.Username
	}

	return ""
}

// OS reports the remote operating system, detected once at connect time.
func (e *Environment) OS() invoke.TargetOS {
	return e.os
}

// Capabilities reports the SSH target's optional features. Terminal
// allocation is available, the protocol carrying a pseudo-terminal
// request natively, and SFTP preserves symbolic links.
//
// Signal delivery is declared with a caveat this provider cannot resolve
// for itself. The protocol carries a signal request, and the servers this
// library is tested against act on it — a real OpenSSH server and the
// in-process one, both of which the signal contracts run against. But the
// request is sent without asking for a reply, there being no answer worth
// waiting for, so a server that discards it cannot be told apart from one
// that obeyed. A container can be asked whether it holds a shell, which
// is why the docker provider conditions this capability on the answer; a
// server offers nothing equivalent to ask.
//
// A server not known to honor signals can be put to the question
// directly: run invoketest.Verify against it and the signal contracts
// report what it actually does, once, rather than every connection
// paying for a guess.
func (e *Environment) Capabilities() invoke.Capabilities {
	return invoke.Capabilities{
		TTY:             true,
		Signals:         true,
		SymlinkPreserve: true,
	}
}

// LookPath resolves name on the remote host via the shell's command -v.
func (e *Environment) LookPath(ctx context.Context, name string) (string, error) {
	if err := e.checkOpen("lookpath"); err != nil {
		return "", err
	}

	out, code, err := e.runRaw(ctx, "command -v "+quoteArg(name))
	if err != nil {
		return "", fmt.Errorf("ssh: lookpath %q: %w", name, err)
	}

	if code != 0 {
		return "", fmt.Errorf("ssh: lookpath %q: %w", name, invoke.ErrNotFound)
	}

	return strings.TrimSpace(out), nil
}

// Close closes the SSH connection, terminating processes still running.
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

	// Stop probing and wait for the loop to finish before the connection
	// goes away, so no probe outlives Close. The wait is bounded: a
	// probe already in flight answers or times out within one interval.
	if e.stopKeepAlive != nil {
		e.stopKeepAlive()
		<-e.keepAliveDone
	}

	for _, p := range procs {
		_ = p.Close()
	}

	err := e.client.Close()

	closeAgent(e.agentConn)

	return err
}

// startKeepAlive probes the server periodically so a connection that dies
// without a close — a dropped link, a NAT timeout — is discovered rather
// than leaving the next operation blocked on a socket nobody is serving.
//
// A probe the server does not answer within one interval is that
// discovery. The client is closed on the spot, which is what unblocks
// everything still waiting on the dead link: running Waits, transfers,
// and Close itself. Without the bound, a probe on a black-holed
// connection would block in its own send until the kernel gave up on
// the socket — and hold Close hostage with it.
func (e *Environment) startKeepAlive() {
	interval := e.cfg.keepAlive()
	if interval <= 0 {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	e.stopKeepAlive = cancel
	e.keepAliveDone = make(chan struct{})

	// The grace is how long an answer may take before the link is
	// declared dead. It is floored: an answer bound tighter than a
	// second measures scheduling noise, not the link.
	grace := max(interval, probeGraceFloor)

	go func() {
		defer close(e.keepAliveDone)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !e.probeAlive(ctx, grace) {
					_ = e.client.Close()

					return
				}
			}
		}
	}()
}

// probeGraceFloor is the least time a probe gets to answer.
const probeGraceFloor = time.Second

// probeAlive sends one keepalive and reports whether the server answered
// within bound. An answer slower than the probing cadence is
// indistinguishable from none; a stopped loop no longer cares either
// way.
func (e *Environment) probeAlive(ctx context.Context, bound time.Duration) bool {
	answered := make(chan error, 1)

	go func() {
		_, _, err := e.client.SendRequest("keepalive@openssh.com", true, nil)
		answered <- err
	}()

	timer := time.NewTimer(bound)
	defer timer.Stop()

	select {
	case err := <-answered:
		return err == nil
	case <-timer.C:
		return false
	case <-ctx.Done():
		// Stopping. Liveness no longer matters, but a probe already on
		// the wire is seen out first — no probe outlives Close — with
		// the same bound, so a dead link cannot hold this open either.
		select {
		case <-answered:
		case <-timer.C:
		}

		return true
	}
}

// detectOS runs uname on the remote host to classify its operating system,
// defaulting to Linux when the answer is unrecognized.
func (e *Environment) detectOS(ctx context.Context) invoke.TargetOS {
	probeCtx, cancel := context.WithTimeout(ctx, e.cfg.timeout())
	defer cancel()

	out, code, err := e.runRaw(probeCtx, "uname -s")
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

func (e *Environment) checkOpen(op string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.closed {
		return fmt.Errorf("ssh: %s: %w", op, invoke.ErrClosed)
	}

	return nil
}

// track registers a running process so Close can terminate it, unless the
// connection has closed in the meantime.
//
// Start checks the closed flag once at entry and then opens a session and
// starts the command, several round-trips later. A Close landing in that
// window has already gathered the processes it will terminate, so one
// added afterwards would run with nothing left to stop it. Re-checking
// here under the same lock closes that gap: a process is either registered
// before Close snapshots, and terminated with the rest, or refused, and
// torn down by its caller.
func (e *Environment) track(p *process) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.closed {
		return fmt.Errorf("ssh: start: %w", invoke.ErrClosed)
	}

	e.active[p] = struct{}{}

	return nil
}

func (e *Environment) untrack(p *process) {
	e.mu.Lock()
	defer e.mu.Unlock()

	delete(e.active, p)
}
