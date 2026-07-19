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

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, clientCfg)
	if err != nil {
		_ = conn.Close()

		closeAgent(agentConn)

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

// Capabilities reports the SSH target's optional features. Signal
// delivery and symlink-preserving transfers are supported.
//
// Terminal allocation is available: the protocol carries a pseudo-
// terminal request natively.
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
	// goes away, so no probe outlives Close.
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
func (e *Environment) startKeepAlive() {
	interval := e.cfg.keepAlive()
	if interval <= 0 {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	e.stopKeepAlive = cancel
	e.keepAliveDone = make(chan struct{})

	go func() {
		defer close(e.keepAliveDone)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// A failed probe means the connection is gone; the
				// operations using it report that themselves, so the
				// loop only needs to stop asking.
				if _, _, err := e.client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
					return
				}
			}
		}
	}()
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
