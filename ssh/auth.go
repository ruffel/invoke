package ssh

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// authMethods builds the ordered list of authentication methods from the
// config. A single unusable method (an unreadable key, an absent agent)
// is skipped with its reason collected, rather than aborting the whole
// connection, so long as some method remains. If none can be assembled,
// the collected reasons are returned.
//
// The returned closer releases any agent connection the methods hold; the
// caller owns it for the life of the connection.
func authMethods(cfg *Config) ([]ssh.AuthMethod, io.Closer, error) {
	var (
		methods   []ssh.AuthMethod
		skipped   []error
		agentConn io.Closer
	)

	if cfg.Password != "" {
		methods = append(methods, ssh.Password(cfg.Password))
	}

	if cfg.PrivateKeyPath != "" {
		if m, err := keyAuth(cfg); err != nil {
			skipped = append(skipped, err)
		} else {
			methods = append(methods, m)
		}
	}

	if cfg.UseAgent {
		m, conn, err := agentAuth()
		if err != nil {
			skipped = append(skipped, err)
		} else {
			methods = append(methods, m)
			agentConn = conn
		}
	}

	if len(methods) == 0 {
		closeAgent(agentConn)

		if len(skipped) > 0 {
			return nil, nil, fmt.Errorf("ssh: no usable authentication method: %w", errors.Join(skipped...))
		}

		return nil, nil, errors.New("ssh: no authentication method configured")
	}

	return methods, agentConn, nil
}

// closeAgent releases an agent connection when one was opened.
func closeAgent(conn io.Closer) {
	if conn != nil {
		_ = conn.Close()
	}
}

// keyAuth loads a private key, decrypting it with the passphrase when one
// is configured.
func keyAuth(cfg *Config) (ssh.AuthMethod, error) {
	raw, err := os.ReadFile(cfg.PrivateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("ssh: reading private key: %w", err)
	}

	var signer ssh.Signer
	if cfg.PrivateKeyPassphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(raw, []byte(cfg.PrivateKeyPassphrase))
	} else {
		signer, err = ssh.ParsePrivateKey(raw)
	}

	if err != nil {
		return nil, fmt.Errorf("ssh: parsing private key %q: %w", cfg.PrivateKeyPath, err)
	}

	return ssh.PublicKeys(signer), nil
}

// agentAuth connects to the SSH agent and offers its keys. The returned
// closer must be closed when the connection is done; the caller tracks it
// on the Environment so the socket is released by Close.
func agentAuth() (ssh.AuthMethod, io.Closer, error) {
	socket := os.Getenv("SSH_AUTH_SOCK")
	if socket == "" {
		return nil, nil, errors.New("ssh: agent requested but SSH_AUTH_SOCK is unset")
	}

	conn, err := net.Dial("unix", socket) //nolint:noctx // Local agent socket; connection is immediate.
	if err != nil {
		return nil, nil, fmt.Errorf("ssh: dialing agent: %w", err)
	}

	client := agent.NewClient(conn)

	return ssh.PublicKeysCallback(client.Signers), conn, nil
}
