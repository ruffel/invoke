//go:build windows

package local

import (
	"fmt"
	"os/exec"
	"time"

	"github.com/ruffel/invoke"
)

// Windows is not a supported execution target this cycle; New refuses to
// construct an Environment there, so these stubs exist only to keep the
// package compiling for cross-builds.

type terminal struct{}

func attachTerminal(_ *exec.Cmd, _ *invoke.TTY) (*terminal, error) {
	return nil, fmt.Errorf("local: start: tty allocation: %w", invoke.ErrNotSupported)
}

func (t *terminal) start(_ *exec.Cmd, _ invoke.IO) {}

func (t *terminal) finish(_ time.Duration) {}

func (t *terminal) close() {}
