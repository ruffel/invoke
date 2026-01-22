package docker

import "net/http"

// Option defines a functional option for the Docker provider.
type Option func(*Config)

// WithConfig returns an Option that sets multiple fields from a Config struct.
func WithConfig(c Config) Option {
	return func(cfg *Config) {
		*cfg = c
	}
}

// WithContainerID sets the target container ID.
func WithContainerID(id string) Option {
	return func(c *Config) {
		c.ContainerID = id
	}
}

// WithHost sets the Docker daemon host.
func WithHost(host string) Option {
	return func(c *Config) {
		c.Host = host
	}
}

// WithVersion sets the Docker API version.
func WithVersion(version string) Option {
	return func(c *Config) {
		c.Version = version
	}
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Config) {
		c.HTTPClient = client
	}
}
