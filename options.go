package invoke

import (
	"os"
	"time"
)

// ExecConfig holds configuration derived from options.
type ExecConfig struct {
	SudoConfig    *SudoConfig
	RetryAttempts int
	RetryDelay    time.Duration
}

// SudoConfig defines advanced privilege escalation options.
type SudoConfig struct {
	User        string   // Target user (-u)
	Group       string   // Target group (-g)
	PreserveEnv bool     // Preserve environment (-E)
	CustomFlags []string // Additional flags
}

// ExecOption defines a functional option for execution.
type ExecOption func(*ExecConfig)

// SudoOption defines a functional option for sudo configuration.
type SudoOption func(*SudoConfig)

// WithSudo wraps the command in sudo.
func WithSudo(opts ...SudoOption) ExecOption {
	return func(c *ExecConfig) {
		if c.SudoConfig == nil {
			c.SudoConfig = &SudoConfig{}
		}
		for _, o := range opts {
			o(c.SudoConfig)
		}
	}
}

// WithSudoUser sets the target user.
func WithSudoUser(user string) SudoOption {
	return func(s *SudoConfig) {
		s.User = user
	}
}

// WithSudoPreserveEnv preserves the environment.
func WithSudoPreserveEnv() SudoOption {
	return func(s *SudoConfig) {
		s.PreserveEnv = true
	}
}

// WithRetry enables retry logic for the command execution using linear backoff.
// attempts: Total number of attempts (including the initial one). Must be >= 1.
// delay: Duration to wait between attempts.
func WithRetry(attempts int, delay time.Duration) ExecOption {
	return func(c *ExecConfig) {
		if attempts < 1 {
			attempts = 1
		}

		c.RetryAttempts = attempts
		c.RetryDelay = delay
	}
}

// FileConfig holds configuration for file transfers.
type FileConfig struct {
	Permissions os.FileMode // Destination perms override (0 means preserve/default)
	UID, GID    int         // Destination ownership (0 usually means root/current)
	Recursive   bool        // Default true for generic uploads
	Progress    ProgressFunc
}

// DefaultFileConfig returns defaults.
func DefaultFileConfig() FileConfig {
	return FileConfig{
		Recursive: true,
	}
}

// FileOption defines a functional option for file transfers.
type FileOption func(*FileConfig)

// WithPermissions forces specific destination file mode.
func WithPermissions(mode os.FileMode) FileOption {
	return func(c *FileConfig) {
		c.Permissions = mode
	}
}

// WithOwner forces specific destination ownership.
func WithOwner(uid, gid int) FileOption {
	return func(c *FileConfig) {
		c.UID = uid
		c.GID = gid
	}
}

// ProgressFunc is a callback for tracking file transfer progress.
type ProgressFunc func(current, total int64)

// WithProgress calls fn with progress updates.
func WithProgress(fn ProgressFunc) FileOption {
	return func(c *FileConfig) {
		c.Progress = fn
	}
}
