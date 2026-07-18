package ssh

import (
	"errors"
	"fmt"

	"github.com/skeema/knownhosts"
	"golang.org/x/crypto/ssh"
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

// fromKnownHosts builds a host-key callback from a known_hosts file, along
// with the host-key algorithms recorded there for this host.
//
// Constraining negotiation to the recorded algorithms is what makes the
// check usable: a server offers its preferred key type, so a host recorded
// under one type would otherwise be reported as an unknown host — the
// warning that means a machine-in-the-middle — merely because it was first
// seen under another.
func fromKnownHosts(path, addr string) (ssh.HostKeyCallback, []string, error) {
	db, err := knownhosts.NewDB(path)
	if err != nil {
		return nil, nil, fmt.Errorf("ssh: loading known_hosts %q: %w", path, err)
	}

	return db.HostKeyCallback(), db.HostKeyAlgorithms(addr), nil
}
