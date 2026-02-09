package ssh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ruffel/invoke"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

var _ invoke.Environment = (*Environment)(nil)

// Environment implements invoke.Environment for SSH execution.
type Environment struct {
	config Config
	client *ssh.Client
	mu     sync.Mutex
	active int
	closed bool
}

// loadPrivateKeyAuth loads a private key from a file and returns an ssh.AuthMethod.
// Returns nil if the path is empty.
func loadPrivateKeyAuth(keyPath string) (ssh.AuthMethod, error) {
	if keyPath == "" {
		return nil, nil //nolint:nilnil // Valid state: no key path provided, so no auth method returned
	}

	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read private key file: %w", err)
	}

	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key file: %w", err)
	}

	return ssh.PublicKeys(signer), nil
}

// loadAgentAuth connects to the SSH agent and returns an ssh.AuthMethod.
// Returns nil if UseAgent is false or the agent socket is unavailable.
func loadAgentAuth(useAgent bool) ssh.AuthMethod {
	if !useAgent {
		return nil
	}

	socket := os.Getenv("SSH_AUTH_SOCK")
	if socket == "" {
		return nil
	}

	conn, err := (&net.Dialer{Timeout: 500 * time.Millisecond}).DialContext(context.Background(), "unix", socket)
	if err != nil {
		return nil
	}

	signers, err := agent.NewClient(conn).Signers()
	if err != nil {
		return nil
	}

	return ssh.PublicKeys(signers...)
}

// New establishes a new SSH connection.
func New(opts ...Option) (*Environment, error) {
	c := Config{}
	for _, opt := range opts {
		opt(&c)
	}

	c = c.WithDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}

	clientConfig, err := c.ToClientConfig()
	if err != nil {
		return nil, err
	}

	if keyAuth, err := loadPrivateKeyAuth(c.PrivateKeyPath); err != nil {
		return nil, err
	} else if keyAuth != nil {
		clientConfig.Auth = append(clientConfig.Auth, keyAuth)
	}

	if agentAuth := loadAgentAuth(c.UseAgent); agentAuth != nil {
		clientConfig.Auth = append(clientConfig.Auth, agentAuth)
	}

	addr := fmt.Sprintf("%s:%d", c.Host, c.Port)

	client, err := ssh.Dial("tcp", addr, clientConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to dial ssh at %s: %w", addr, err)
	}

	return NewFromClient(client, c), nil
}

// NewFromClient creates a new SSH environment from an existing client.
func NewFromClient(client *ssh.Client, config Config) *Environment {
	return &Environment{
		config: config,
		client: client,
	}
}

// Run executes a command synchronously on the remote server.
func (e *Environment) Run(ctx context.Context, cmd *invoke.Command) (*invoke.Result, error) {
	proc, err := e.Start(ctx, cmd)
	if err != nil {
		return nil, err
	}

	defer func() { _ = proc.Close() }()

	waitErr := proc.Wait()
	// Always return the result if available, even if Wait failed (e.g. non-zero exit code)
	if res := proc.Result(); res != nil {
		return res, waitErr
	}

	return nil, waitErr
}

// Start opens a NEW SSH session for the command.
func (e *Environment) Start(ctx context.Context, cmd *invoke.Command) (invoke.Process, error) {
	e.mu.Lock()

	if e.closed {
		e.mu.Unlock()

		return nil, errors.New("ssh environment closed")
	}

	e.active++
	e.mu.Unlock()

	session, err := e.client.NewSession()
	if err != nil {
		e.mu.Lock()
		e.active--
		e.mu.Unlock()

		return nil, fmt.Errorf("failed to create ssh session: %w", err)
	}

	process := &Process{
		env:     e,
		session: session,
		cmd:     cmd,
		done:    make(chan struct{}),
	}

	if err := process.start(ctx); err != nil {
		_ = session.Close()

		e.mu.Lock()
		e.active--
		e.mu.Unlock()

		return nil, err
	}

	return process, nil
}

// TargetOS returns the operating system as configured.
func (e *Environment) TargetOS() invoke.TargetOS {
	return e.config.OS
}

// Close closes the underlying SSH connection.
func (e *Environment) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.closed {
		return nil
	}

	e.closed = true

	if e.client != nil {
		return e.client.Close()
	}

	return nil
}

// LookPath searches for an executable on the remote host using 'command -v'.
func (e *Environment) LookPath(ctx context.Context, file string) (string, error) {
	cmdStr := "command"
	args := []string{"-v", file}

	if e.TargetOS() == invoke.OSWindows {
		cmdStr = "where"
		args = []string{file}
	}

	var stdout strings.Builder

	cmd := &invoke.Command{
		Cmd:    cmdStr,
		Args:   args,
		Stdout: &stdout,
	}

	result, err := e.Run(ctx, cmd)
	if err != nil {
		return "", err
	}

	if result.ExitCode != 0 {
		return "", &invoke.ExitError{Command: cmd, ExitCode: result.ExitCode}
	}

	// Windows 'where' might return multiple lines, take the first one
	output := strings.TrimSpace(stdout.String())
	if e.TargetOS() == invoke.OSWindows {
		lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
		if len(lines) > 0 {
			return strings.TrimSpace(lines[0]), nil
		}

		return "", &invoke.ExitError{Command: cmd, ExitCode: 1}
	}

	return output, nil
}

func (e *Environment) decrementActive() {
	e.mu.Lock()
	e.active--
	e.mu.Unlock()
}
