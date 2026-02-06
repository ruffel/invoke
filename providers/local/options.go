package local

import "github.com/ruffel/invoke"

// Config holds configuration for the local environment.
// Currently minimal but allows for future extensibility (e.g. custom shell).
type Config struct {
	targetOS invoke.TargetOS
}

// Option defines a functional option for the local provider.
type Option func(*Config)

// API compatibility check.
var _ invoke.Environment = (*Environment)(nil)
