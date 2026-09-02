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
// config: public keys first, the password last, the order OpenSSH uses.
// A key proves itself without leaving the client; a password is a secret
// the server gets to keep. So the keys go first, and the password is sent
// only when none of them was accepted.
//
// A single unusable method (an unreadable key, an absent agent) is
// skipped with its reason collected, rather than aborting the whole
// connection, so long as some method remains. If none can be assembled,
// the collected reasons are returned.
//
// The key file and the agent make up one method, not two. The protocol
// names both "publickey", and a client offers each name once, so
// registering them separately would leave the agent unasked whenever the
// file key was refused. Their keys are pooled instead, the file's first.
//
// The returned closer releases any agent connection the methods hold; the
// caller owns it for the life of the connection.
func authMethods(cfg *Config) ([]ssh.AuthMethod, io.Closer, error) {
	var (
		methods   []ssh.AuthMethod
		signers   []ssh.Signer
		skipped   []error
		agentConn io.Closer
	)

	if cfg.PrivateKeyPath != "" {
		if s, err := keySigner(cfg); err != nil {
			skipped = append(skipped, err)
		} else {
			signers = append(signers, s)
		}
	}

	if cfg.UseAgent {
		held, conn, err := agentSigners()
		if err != nil {
			skipped = append(skipped, err)
		} else {
			signers = append(signers, held...)
			agentConn = conn
		}
	}

	if len(signers) > 0 {
		methods = append(methods, ssh.PublicKeys(signers...))
	}

	if cfg.Password != "" {
		methods = append(methods, ssh.Password(cfg.Password))
	}

	if len(methods) == 0 {
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

// keySigner loads the private key file, decrypting it with the passphrase
// when one is configured.
func keySigner(cfg *Config) (ssh.Signer, error) {
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

	return signer, nil
}

// agentSigners connects to the SSH agent at SSH_AUTH_SOCK and lists the
// keys it holds. Each signs through the connection, so the returned closer
// must outlive authentication; the caller tracks it on the Environment so
// Close releases the socket. When no key can be offered, the socket is
// already closed.
func agentSigners() ([]ssh.Signer, io.Closer, error) {
	socket := os.Getenv("SSH_AUTH_SOCK")
	if socket == "" {
		return nil, nil, errors.New("ssh: agent requested but SSH_AUTH_SOCK is unset")
	}

	conn, err := net.Dial("unix", socket) //nolint:noctx // Local agent socket; connection is immediate.
	if err != nil {
		return nil, nil, fmt.Errorf("ssh: dialing agent: %w", err)
	}

	signers, err := agent.NewClient(conn).Signers()
	if err != nil {
		_ = conn.Close()

		return nil, nil, fmt.Errorf("ssh: listing agent keys: %w", err)
	}

	if len(signers) == 0 {
		_ = conn.Close()

		return nil, nil, errors.New("ssh: agent holds no keys")
	}

	return signers, conn, nil
}
