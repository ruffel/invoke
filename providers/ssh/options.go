package ssh

// Option defines a functional option for the SSH provider.
type Option func(*Config)

// WithConfig returns an Option that sets multiple fields from a Config struct.
// Useful for legacy compatibility or bulk configuration.
func WithConfig(c Config) Option {
	return func(cfg *Config) {
		*cfg = c
	}
}

// WithHost sets the target hostname.
func WithHost(host string) Option {
	return func(c *Config) {
		c.Host = host
	}
}

// WithUser sets the SSH user.
func WithUser(user string) Option {
	return func(c *Config) {
		c.User = user
	}
}

// WithPort sets the SSH port.
func WithPort(port int) Option {
	return func(c *Config) {
		c.Port = port
	}
}

// WithPassword sets the SSH password.
func WithPassword(password string) Option {
	return func(c *Config) {
		c.Password = password
	}
}

// WithKeyPath sets the path to the private key file.
func WithKeyPath(path string) Option {
	return func(c *Config) {
		c.PrivateKeyPath = path
	}
}

// WithInsecureSkipVerify enables/disables strict host key checking.
func WithInsecureSkipVerify(skip bool) Option {
	return func(c *Config) {
		c.InsecureSkipVerify = skip
	}
}
