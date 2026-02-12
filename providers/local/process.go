package local

import (
	"os/exec"
	"sync"

	"github.com/ruffel/invoke"
)

// Process implements invoke.Process for local command execution.
// It wraps `*exec.Cmd` to provide a uniform interface for waiting, signaling, and result retrieval.
type Process struct {
	env     *Environment
	cmd     *invoke.Command
	execCmd *exec.Cmd

	// Result related fields
	result *invoke.Result
	mu     sync.RWMutex
	done   chan struct{}
	closed bool
}
