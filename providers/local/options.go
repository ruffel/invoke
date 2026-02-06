package local

import "github.com/ruffel/invoke"

// Config holds configuration for the local environment.
// Currently empty but allows for future extensibility (e.g. custom shell).
type Config struct {
	TargetOS invoke.TargetOS
}

// Option defines a functional option for the local provider.
type Option func(*Config)

// API compatibility check.
var _ invoke.Environment = (*Environment)(nil)
