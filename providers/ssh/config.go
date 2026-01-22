package ssh

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/kevinburke/ssh_config"
	"github.com/ruffel/invoke"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Config holds all parameters required to establish an SSH connection.
type Config struct {
	// Connection details
	Host string // Hostname or IP address
	Port int    // Port number (default 22)
	User string // Username to authenticate as

	// Authentication methods (tried in order)
	PrivateKey     string // PEM encoded private key content (string)
	PrivateKeyPath string // Path to private key file (e.g. "~/.ssh/id_rsa")
	Password       string // Password for authentication (use sparingly)
	UseAgent       bool   // If true, attempt to connect to SSH_AUTH_SOCK

	// Connection settings
	Timeout            time.Duration       // Connection timeout (default 10s)
	HostKeyCheck       ssh.HostKeyCallback // Callback to verify host key. You normally generate this from known_hosts.
	InsecureSkipVerify bool                // If true, disables strict host key checking. Use ONLY for testing.
	OS                 invoke.TargetOS     // Target operating system (default OSLinux). Used for path separators contexts.
}

// NewConfig creates a Config with safe defaults.
// Note: It does NOT set a default HostKeyCheck. You must provide one or set InsecureSkipVerify=true.
func NewConfig(host, username string) Config {
	return Config{
		Host:    host,
		User:    username,
		Port:    22,
		Timeout: 10 * time.Second,
	}
}

// NewFromSSHConfig loads configuration from an SSH config file (e.g. ~/.ssh/config).
// logic mirrors OpenSSH: reads specific path or default ~/.ssh/config.
func NewFromSSHConfig(alias, path string) (Config, error) {
	f, err := os.Open(filepath.Join(os.Getenv("HOME"), ".ssh", "config"))
	if path != "" {
		f, err = os.Open(path)
	}

	if err != nil {
		return Config{}, fmt.Errorf("failed to open ssh config: %w", err)
	}

	defer func() { _ = f.Close() }()

	return NewFromSSHConfigReader(alias, f)
}

// NewFromSSHConfigReader parses configuration config data.
// It resolves the alias to the actual HostName, User, Port, and IdentityFile.
func NewFromSSHConfigReader(alias string, r io.Reader) (Config, error) {
	cfg, err := ssh_config.Decode(r)
	if err != nil {
		return Config{}, fmt.Errorf("failed to parse ssh config: %w", err)
	}

	hostName, err := cfg.Get(alias, "HostName")
	if err != nil || hostName == "" {
		hostName = alias // Fallback if no HostName defined
	}

	username, _ := cfg.Get(alias, "User")
	if username == "" {
		// Use current system user if not specified in config
		u, _ := user.Current()
		if u != nil {
			username = u.Username
		}
	}

	portStr, _ := cfg.Get(alias, "Port")

	port := 22
	if portStr != "" {
		_, _ = fmt.Sscanf(portStr, "%d", &port)
	}

	identityFile, _ := cfg.Get(alias, "IdentityFile")
	if strings.HasPrefix(identityFile, "~/") {
		identityFile = filepath.Join(os.Getenv("HOME"), identityFile[2:])
	}

	c := NewConfig(hostName, username)
	c.Port = port
	c.PrivateKeyPath = identityFile

	// Map StrictHostKeyChecking
	strict, _ := cfg.Get(alias, "StrictHostKeyChecking")
	if strict == "no" {
		c.InsecureSkipVerify = true
	}

	return c, nil
}

// WithDefaults sets default values for zero-valued fields.
func (c Config) WithDefaults() Config {
	if c.Host != "" && c.User != "" && c.Port == 0 {
		c.Port = 22
	}

	if c.Timeout == 0 {
		c.Timeout = 10 * time.Second
	}

	// If insecure is requested and no callback provided, use insecure ignore.
	if c.InsecureSkipVerify && c.HostKeyCheck == nil {
		c.HostKeyCheck = ssh.InsecureIgnoreHostKey()
	}

	if c.OS == invoke.OSUnknown {
		c.OS = invoke.OSLinux
	}

	return c
}

// Validate ensures all required fields are present.
func (c Config) Validate() error {
	if c.Host == "" {
		return errors.New("configuration error: host address cannot be empty")
	}

	if c.User == "" {
		return errors.New("configuration error: user cannot be empty")
	}

	if c.HostKeyCheck == nil {
		return errors.New("configuration error: HostKeyCheck is missing; you must provide a callback (e.g. valid 'known_hosts') or set InsecureSkipVerify=true (testing only)")
	}

	return nil
}

// ToClientConfig converts the local Config struct to the underlying ssh.ClientConfig.
func (c Config) ToClientConfig() (*ssh.ClientConfig, error) {
	config := &ssh.ClientConfig{
		User:            c.User,
		Auth:            []ssh.AuthMethod{},
		HostKeyCallback: c.HostKeyCheck,
		Timeout:         c.Timeout,
	}

	// Add auth methods
	if c.Password != "" {
		config.Auth = append(config.Auth, ssh.Password(c.Password))
	}

	if c.PrivateKey != "" {
		signer, err := ssh.ParsePrivateKey([]byte(c.PrivateKey))
		if err != nil {
			return nil, fmt.Errorf("failed to parse private key: %w", err)
		}

		config.Auth = append(config.Auth, ssh.PublicKeys(signer))
	}

	return config, nil
}

// DefaultKnownHosts returns a HostKeyCallback that verifies the host key against
// strict entries in the user's ~/.ssh/known_hosts file.
func DefaultKnownHosts() (ssh.HostKeyCallback, error) {
	path := filepath.Join(os.Getenv("HOME"), ".ssh", "known_hosts")

	return knownhosts.New(path)
}
