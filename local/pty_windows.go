//go:build windows

package local

import (
	"fmt"
	"os/exec"
	"time"

	"github.com/ruffel/invoke"
)

// Compile-only stubs: see the package doc on Windows.

type terminal struct{}

func attachTerminal(_ *exec.Cmd, _ *invoke.TTY) (*terminal, error) {
	return nil, fmt.Errorf("local: start: tty allocation: %w", invoke.ErrNotSupported)
}

func (t *terminal) start(_ invoke.IO) {}

func (t *terminal) finish(_ time.Duration) {}

func (t *terminal) close() {}
