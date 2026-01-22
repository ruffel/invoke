package local

import "github.com/ruffel/invoke"

// Config holds configuration for the local environment.
type Config struct {
	targetOS invoke.TargetOS
}

// Option defines a functional option for the local provider.
type Option func(*Config)
