// Package local executes commands and transfers files on the machine the
// program is running on. It is the reference [invoke.Environment]: every
// behavior it implements is pinned by the invoketest contract suite.
//
// Windows is not a supported execution target this cycle; the package
// compiles there, but [New] returns an error wrapping
// [invoke.ErrNotSupported].
//
// A command asked for a terminal gets a real one: its own session with a
// controlling terminal, both output streams merged onto it, and the
// dimensions the caller requested.
package local

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"

	"github.com/ruffel/invoke"
)

// Environment runs commands on the local machine.
type Environment struct {
	cfg config

	mu     sync.Mutex
	closed bool
	active map[*process]struct{}
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

	return &Environment{cfg: cfg, active: make(map[*process]struct{})}, nil
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

// Close marks the environment closed and terminates any processes still
// running. It is idempotent.
func (e *Environment) Close() error {
	e.mu.Lock()

	if e.closed {
		e.mu.Unlock()

		return nil
	}

	e.closed = true

	procs := make([]*process, 0, len(e.active))
	for p := range e.active {
		procs = append(procs, p)
	}

	e.mu.Unlock()

	for _, p := range procs {
		_ = p.Close()
	}

	return nil
}

// LookPath resolves name against the host process's PATH.
func (e *Environment) LookPath(ctx context.Context, name string) (string, error) {
	if err := e.checkOpen("lookpath"); err != nil {
		return "", err
	}

	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("local: lookpath: %w", err)
	}

	path, err := exec.LookPath(name)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", fmt.Errorf("local: lookpath %q: %w", name, invoke.ErrNotFound)
		}

		return "", fmt.Errorf("local: lookpath %q: %w", name, err)
	}

	return path, nil
}

func (e *Environment) checkOpen(op string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.closed {
		return fmt.Errorf("local: %s: %w", op, invoke.ErrClosed)
	}

	return nil
}

// track registers a running process so Close can terminate it, unless the
// environment has closed in the meantime.
//
// Start checks the closed flag once at entry and then starts the process
// and wires its streams. A Close landing in that window has already
// gathered the processes it will terminate, so one added afterwards would
// run with nothing left to stop it. Re-checking here under the same lock
// closes that gap: a process is either registered before Close snapshots,
// and terminated with the rest, or refused, and torn down by its caller.
func (e *Environment) track(p *process) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.closed {
		return fmt.Errorf("local: start: %w", invoke.ErrClosed)
	}

	e.active[p] = struct{}{}

	return nil
}

func (e *Environment) untrack(p *process) {
	e.mu.Lock()
	defer e.mu.Unlock()

	delete(e.active, p)
}

// dirExists reports whether path names an existing directory; it backs the
// workdir pre-check so a bad Dir classifies as ErrInvalidWorkdir instead of
// surfacing as an ambiguous exec failure.
func dirExists(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.IsDir()
}
