package docker

import (
	"errors"
	"net/http"

	"github.com/docker/docker/client"
	"github.com/ruffel/invoke"
)

// Config holds configuration parameters for establishing a Docker environment connection.
type Config struct {
	// ContainerID is the target container (name or ID) where commands will be executed.
	ContainerID string

	// Host specifies the Docker daemon host (e.g. "unix:///var/run/docker.sock", "ssh://user@host").
	// If empty, it defaults to the value of DOCKER_HOST env var.
	Host string
	// Version specifies the Docker API version to use.
	// If empty, version negotiation is used.
	Version string
	// HTTPClient allows providing a custom *http.Client (e.g. for TLS config).
	HTTPClient *http.Client

	// OS specifies the target container's operating system (default: Linux).
	// Set to OSWindows if targeting a Windows container.
	OS invoke.TargetOS
}

// NewConfig creates a simple configuration for a target container.
func NewConfig(containerID string) Config {
	return Config{
		ContainerID: containerID,
	}
}

// Validate checks if the minimal required configuration is present.
func (c Config) Validate() error {
	if c.ContainerID == "" {
		return errors.New("ContainerID is required")
	}

	return nil
}

// ClientOpts converts the Config struct into a slice of Docker Client options.
// This is used internally to initialize the docker client.
func (c Config) ClientOpts() []client.Opt {
	opts := []client.Opt{
		client.FromEnv, // Respect DOCKER_HOST, DOCKER_TLS_VERIFY, etc from env
		client.WithAPIVersionNegotiation(),
	}

	if c.Host != "" {
		opts = append(opts, client.WithHost(c.Host))
	}

	if c.Version != "" {
		opts = append(opts, client.WithVersion(c.Version))
	}

	if c.HTTPClient != nil {
		opts = append(opts, client.WithHTTPClient(c.HTTPClient))
	}

	return opts
}
