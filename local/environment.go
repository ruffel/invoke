// Package local executes commands and transfers files on the machine the
// program is running on. It is the reference [invoke.Environment]: every
// behavior it implements is pinned by the invoketest contract suite.
//
// Windows is a compile target, not an execution target: the package
// builds there so programs importing it cross-compile, but [New] returns
// an error wrapping [invoke.ErrNotSupported], and its Windows-only files
// are stubs that exist only to keep it compiling.
//
// A command asked for a terminal gets a real one: its own session with a
// controlling terminal, both output streams merged onto it, and the
// dimensions the caller requested.
package local

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"runtime"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/internal/lifetime"
)

// Environment runs commands on the local machine.
type Environment struct {
	cfg config

	// life is what Close ends: the processes still running and the
	// transfers in flight.
	life *lifetime.Lifetime
}

var _ invoke.Environment = (*Environment)(nil)

// New returns an Environment for the local machine.
func New(opts ...Option) (*Environment, error) {
	if runtime.GOOS == "windows" {
		return nil, fmt.Errorf("local: windows execution targets: %w", invoke.ErrNotSupported)
	}

	var cfg config
	for _, opt := range opts {
		opt(&cfg)
	}

	return &Environment{cfg: cfg, life: lifetime.New()}, nil
}

// OS reports the local operating system.
func (e *Environment) OS() invoke.TargetOS {
	return invoke.LocalOS()
}

// Capabilities reports the local target's optional features. All of them
// are available: signal delivery, symlink-preserving transfers, and
// terminal allocation.
func (e *Environment) Capabilities() invoke.Capabilities {
	return invoke.Capabilities{
		TTY:             true,
		Signals:         true,
		SymlinkPreserve: true,
	}
}

// Close stops any transfer in flight and terminates any process still
// running, waiting for both. It is idempotent.
func (e *Environment) Close() error {
	e.life.Close()

	return nil
}

// LookPath resolves name against the host process's PATH.
func (e *Environment) LookPath(ctx context.Context, name string) (string, error) {
	if err := e.life.Err(); err != nil {
		return "", fmt.Errorf("local: lookpath: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("local: lookpath: %w", err)
	}

	path, err := exec.LookPath(name)
	if err != nil {
		// A bare name missing from PATH reports exec.ErrNotFound; a name
		// with a separator is looked up as a path, so a missing or
		// non-executable file reports the filesystem's own error instead
		// — and a directory reports its own errno again. All of them
		// mean the same thing to a caller: the name did not resolve to
		// something runnable.
		if errors.Is(err, exec.ErrNotFound) ||
			errors.Is(err, fs.ErrNotExist) ||
			errors.Is(err, fs.ErrPermission) ||
			namesDirectory(name) {
			return "", fmt.Errorf("local: lookpath %q: %w", name, invoke.ErrNotFound)
		}

		return "", fmt.Errorf("local: lookpath %q: %w", name, err)
	}

	return path, nil
}

// namesDirectory reports whether name is a directory on disk, the one
// unrunnable shape whose lookup error carries no portable sentinel.
func namesDirectory(name string) bool {
	info, err := os.Stat(name)

	return err == nil && info.IsDir()
}

// usableDir reports whether path names a directory the command could
// enter; it backs the workdir pre-check so a bad Dir classifies as
// ErrInvalidWorkdir instead of surfacing as an exec failure that blames
// the binary. Existing is half the promise: a directory that "cannot be
// entered" — sealed by its mode — fails the same way.
func usableDir(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}

	return dirEnterable(path)
}
