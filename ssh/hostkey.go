package ssh

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// resolveHostKey produces the host-key callback and the host-key algorithm
// list for a connection to addr (host:port). It is fail-closed: with no
// explicit callback, no known_hosts source, and no insecure override, it
// returns an error rather than trusting the server blindly.
func resolveHostKey(cfg *Config, addr string) (ssh.HostKeyCallback, []string, error) {
	switch {
	case cfg.insecureHostKey:
		return ssh.InsecureIgnoreHostKey(), nil, nil //nolint:gosec // Explicit opt-in via WithInsecureIgnoreHostKey.

	case cfg.HostKeyCallback != nil:
		return cfg.HostKeyCallback, nil, nil

	case cfg.knownHostsPath != "":
		return fromKnownHosts(cfg.knownHostsPath, addr)
	}

	// No source configured: fail closed rather than trust on first use.
	return nil, nil, errors.New(
		"ssh: no host-key verification configured; set WithKnownHosts or WithHostKeyCallback " +
			"(or WithInsecureIgnoreHostKey for tests)")
}

// fromKnownHosts builds a host-key callback from a known_hosts file.
//
// The negotiated host-key algorithms are left at the SSH defaults for now;
// constraining them to the key types recorded for the host (which avoids a
// mismatch error when a host is known under one key type but the server
// offers another) lands with the provider hardening work.
func fromKnownHosts(path, _ string) (ssh.HostKeyCallback, []string, error) {
	cb, err := knownhosts.New(path)
	if err != nil {
		return nil, nil, fmt.Errorf("ssh: loading known_hosts %q: %w", path, err)
	}

	return cb, nil, nil
}
