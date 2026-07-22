package docker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ruffel/invoke"
)

// defaultContextName is the built-in context, which has no stored
// endpoint: it means whatever the daemon's own defaults are.
const defaultContextName = "default"

// resolveHost works out which daemon to talk to, in the order the docker
// command itself uses.
//
// Installations that do not run a daemon at the conventional socket —
// Colima, Rancher Desktop, Docker Desktop on some setups, a remote host —
// record where theirs is in a context rather than in the environment.
// Consulting only the environment finds nothing and reports that the
// daemon is not running, which is both wrong and hard to act on.
//
// An empty result means the client's own defaults apply.
func resolveHost(cfg *Config) (string, error) {
	// An explicit endpoint is the caller's decision and outranks
	// everything, as does the environment the docker command reads first.
	if cfg.Host != "" {
		if err := checkEndpoint(cfg.Host); err != nil {
			return "", err
		}

		return cfg.Host, nil
	}

	if host := os.Getenv("DOCKER_HOST"); host != "" {
		if err := checkEndpoint(host); err != nil {
			return "", err
		}

		return "", nil
	}

	name, explicit := contextName(cfg)
	if name == "" || name == defaultContextName {
		return "", nil
	}

	host, err := contextHost(name)
	if err != nil {
		// A named context that cannot be read is worth reporting rather
		// than quietly falling back: the fallback is a different daemon,
		// and commands would run somewhere the caller did not choose.
		if explicit || !errors.Is(err, os.ErrNotExist) {
			return "", err
		}

		return "", nil
	}

	return host, nil
}

// contextName reports the context to use and whether it was named
// deliberately, rather than inherited from the stored configuration.
func contextName(cfg *Config) (string, bool) {
	if cfg.Context != "" {
		return cfg.Context, true
	}

	if name := os.Getenv("DOCKER_CONTEXT"); name != "" {
		return name, true
	}

	var stored struct {
		CurrentContext string `json:"currentContext"`
	}

	data, err := os.ReadFile(filepath.Join(configDir(), "config.json"))
	if err != nil {
		return "", false
	}

	if err := json.Unmarshal(data, &stored); err != nil {
		return "", false
	}

	return stored.CurrentContext, false
}

// contextHost reads the endpoint a context records for the daemon.
//
// Contexts are stored under a directory named for the digest of the
// context's name, which is how the docker command finds them too.
func contextHost(name string) (string, error) {
	digest := sha256.Sum256([]byte(name))
	dir := filepath.Join(configDir(), "contexts", "meta", hex.EncodeToString(digest[:]))

	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return "", fmt.Errorf("docker: reading context %q: %w", name, err)
	}

	var meta struct {
		Endpoints struct {
			Docker struct {
				Host string `json:"Host"`
			} `json:"docker"`
		} `json:"Endpoints"`
	}

	if err := json.Unmarshal(data, &meta); err != nil {
		return "", fmt.Errorf("docker: reading context %q: %w", name, err)
	}

	if meta.Endpoints.Docker.Host == "" {
		return "", fmt.Errorf("docker: context %q records no endpoint", name)
	}

	// A context can carry client certificates for a daemon reached over
	// TLS. Connecting without them would fail in a way that names the
	// wrong cause, so say plainly what is missing.
	if tlsMaterial(name) {
		return "", fmt.Errorf(
			"docker: context %q uses TLS, which this provider does not read; "+
				"pass the endpoint and credentials explicitly: %w", name, invoke.ErrNotSupported)
	}

	if err := checkEndpoint(meta.Endpoints.Docker.Host); err != nil {
		return "", fmt.Errorf("docker: context %q: %w", name, err)
	}

	return meta.Endpoints.Docker.Host, nil
}

// checkEndpoint refuses a daemon endpoint this provider cannot reach on
// its own. The docker command reaches an ssh:// daemon through a helper
// process; without it, connecting fails deep in the client with an error
// that names the transport rather than the cause, so it is named here.
func checkEndpoint(host string) error {
	if strings.HasPrefix(host, "ssh://") {
		return fmt.Errorf(
			"docker: endpoint %q uses ssh://, which this provider does not support; "+
				"expose the daemon over tcp:// or a unix socket instead: %w", host, invoke.ErrNotSupported)
	}

	return nil
}

// tlsMaterial reports whether a context stores client certificates.
func tlsMaterial(name string) bool {
	digest := sha256.Sum256([]byte(name))
	dir := filepath.Join(configDir(), "contexts", "tls", hex.EncodeToString(digest[:]), "docker")

	entries, err := os.ReadDir(dir)

	return err == nil && len(entries) > 0
}

// configDir is where the docker command keeps its configuration.
func configDir() string {
	if dir := os.Getenv("DOCKER_CONFIG"); dir != "" {
		return dir
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ".docker"
	}

	return filepath.Join(home, ".docker")
}
