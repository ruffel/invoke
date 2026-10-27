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
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/internal/lifetime"
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

	// keepAlive probes the connection for as long as it lives, and is nil
	// when probing is disabled.
	keepAlive *keepAlive

	// life is what Close ends: the commands still running and the
	// transfers in flight.
	life *lifetime.Lifetime
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
		life:   lifetime.New(),
	}

	env.os = env.detectOS(ctx)

	// Deliberately not the caller's context: the probe loop belongs to
	// the connection, which outlives whatever was being done when it was
	// opened. Ending it with that work would leave the connection
	// unwatched for the rest of its life.
	//nolint:contextcheck // The loop's lifetime is the connection's; Close ends it.
	env.keepAlive = startKeepAlive(env.chain, cfg.keepAlive())

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
// in-process one, both of which the signal contracts run against. The
// request asks for a reply, so a server that refuses it is reported,
// wrapping [invoke.ErrNotSupported]; but a reply is the server's word,
// and one that confirms a signal it then discards cannot be told apart
// from one that obeyed. A container can be asked whether it holds a
// shell, which is why the docker provider conditions this capability on
// the answer; a server offers nothing equivalent to ask.
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

// LookPath resolves name on the remote host by walking the login PATH.
func (e *Environment) LookPath(ctx context.Context, name string) (string, error) {
	if err := e.life.Err(); err != nil {
		return "", fmt.Errorf("ssh: lookpath: %w", err)
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

// Close closes the SSH connection, stopping transfers in flight and
// terminating commands still running first. It is idempotent, and
// returns only once the connection is closed, whichever call closed it.
func (e *Environment) Close() error {
	return e.life.CloseWith(e.teardown)
}

// teardown drops the connection once nothing runs over it.
//
// The lifetime has stopped the transfers and terminated the commands by
// the time this runs, in that order, and the order matters. Transfers go
// first, while the connection they unwind over is still there: a stopped
// copy removes its temp file on the way out. The commands go next, while
// the keepalive is still watching: each Close kills its command and
// waits for the outcome, and on a link that has died without a close
// nothing answers either, so the keepalive discovering the dead link and
// closing it is what ends those waits. Only then does probing stop, and
// the loop is waited for before the connection goes, so no probe outlives
// Close.
func (e *Environment) teardown() error {
	e.keepAlive.stop()

	return e.chain.Close()
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

// runRaw runs a raw command line on a fresh session and returns its stdout
// and exit code. It backs everything the provider runs for itself:
// LookPath, OS detection, the pre-flight check, and the removal of an
// environment file.
func (e *Environment) runRaw(ctx context.Context, cmdline string) (string, int, error) {
	session, err := e.client.NewSession()
	if err != nil {
		return "", -1, &invoke.TransportError{Op: "session", Err: err}
	}

	defer func() { _ = session.Close() }()

	var out bytes.Buffer

	session.Stdout = &out

	done := make(chan error, 1)

	go func() { done <- session.Run(cmdline) }()

	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		_ = session.Close()

		<-done

		return out.String(), -1, ctx.Err()
	case runErr := <-done:
		return classifyRaw(out.String(), runErr)
	}
}

// classifyRaw turns a session.Run result into (stdout, exit code, error).
func classifyRaw(out string, err error) (string, int, error) {
	if err == nil {
		return out, 0, nil
	}

	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		return out, exitErr.ExitStatus(), nil
	}

	return out, -1, &invoke.TransportError{Op: "run", Err: err}
}
