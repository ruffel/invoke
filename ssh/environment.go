// Package ssh executes commands and transfers files on a remote host over
// SSH. It implements [invoke.Environment], verified against the invoketest
// contract suite.
//
// Commands are delivered to the remote login shell as a single, shell-safe
// command line (the SSH protocol carries a command string, not an argv),
// with environment variables sent out of band so they do not appear in the
// remote process table. The login shell is assumed POSIX-compatible:
// command lines, pre-flight checks, and environment prologues are plain
// sh, and a csh-family login shell will misread them. Host-key
// verification is fail-closed: a connection requires known_hosts, an
// explicit callback, or an explicit insecure override — and where
// [WithJumpHost] routes a connection through an intermediate host, each
// hop is verified and authenticated on its own terms.
//
// Configuration comes from the options passed here and nowhere else. This
// package does not read ssh_config, so aliases, ProxyJump directives, and
// identity files recorded there have no effect on a connection made with
// it.
package ssh

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ruffel/invoke"
	"golang.org/x/crypto/ssh"
)

// Environment is a connection to a remote host over SSH.
type Environment struct {
	cfg *Config
	os  invoke.TargetOS

	// chain is every connection the target is reached through, the last of
	// which is the target itself. A direct connection is a chain of one.
	chain *chain

	// client is the chain's target, which is what every session runs on.
	client *ssh.Client

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
// ctx bounds establishing the connection only — every hop of it, where
// [WithJumpHost] routes through an intermediate host. It does not govern
// the Environment afterwards, which lives until Close.
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
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	established, err := establish(ctx, cfg)
	if err != nil {
		return nil, err
	}

	env := &Environment{
		cfg:    cfg,
		chain:  established,
		client: established.target(),
		active: make(map[*process]struct{}),
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

// OS reports the remote operating system, detected once at connect time.
func (e *Environment) OS() invoke.TargetOS {
	return e.os
}

// Capabilities reports the SSH target's optional features. Terminal
// allocation is available — the protocol carries a pseudo-terminal
// request natively, though a server configured with PermitTTY no
// refuses it, and that refusal is reported wrapping
// [invoke.ErrNotSupported] rather than retried. SFTP preserves symbolic
// links.
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

	out, code, err := e.runRaw(ctx, lookPathLine(name))
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

	return e.chain.Close()
}

// startKeepAlive probes the server periodically so a connection that dies
// without a close — a dropped link, a NAT timeout — is discovered rather
// than leaving the next operation blocked on a socket nobody is serving.
//
// A probe the server does not answer within one interval is that
// discovery. The connection is closed on the spot, which is what unblocks
// everything still waiting on the dead link: running Waits, transfers,
// and Close itself. Without the bound, a probe on a black-holed
// connection would block in its own send until the kernel gave up on
// the socket — and hold Close hostage with it.
//
// One loop covers a whole chain. A probe to the target crosses every hop
// on the way, so a jump host that dies silently strands the probe exactly
// as a dead target does, and is discovered on the same interval.
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
					// The whole chain goes, not just the target: a link
					// that died takes every connection riding on it with
					// it, and the socket underneath would otherwise be
					// held until Close.
					_ = e.chain.Close()

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

// detectOS runs uname on the remote host to classify its operating
// system. A system it does not recognize — or a probe that fails — is
// reported as undetermined: "probably Linux" is a guess, and the
// taxonomy has an honest value for not knowing.
func (e *Environment) detectOS(ctx context.Context) invoke.TargetOS {
	probeCtx, cancel := context.WithTimeout(ctx, e.cfg.timeout())
	defer cancel()

	out, code, err := e.runRaw(probeCtx, "uname -s")
	if err != nil || code != 0 {
		return invoke.OSUnknown
	}

	switch strings.TrimSpace(out) {
	case "Darwin":
		return invoke.OSDarwin
	case "Linux":
		return invoke.OSLinux
	default:
		return invoke.OSUnknown
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
