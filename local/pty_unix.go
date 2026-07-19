//go:build unix

package local

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/ruffel/invoke"
)

// terminal is the caller's end of a pseudo-terminal, and the goroutines
// moving bytes across it.
type terminal struct {
	primary *os.File
	copied  chan struct{}
}

// attachTerminal gives cmd a pseudo-terminal of the requested size and
// returns the caller's end of it.
//
// A terminal replaces all three streams at once: the command reads from
// it, writes to it, and has its standard error merged into the same
// stream, because that is what a terminal is. It also becomes the
// command's controlling terminal, which is what makes job control and
// keyboard signals behave as they would for a user at a shell.
//
// Setting a session here takes the place of setting a process group: a
// session leader starts a new group whose id is its own process id, so
// signalling the group works exactly as it does without a terminal.
func attachTerminal(cmd *exec.Cmd, requested *invoke.TTY) (*terminal, error) {
	primary, secondary, err := pty.Open()
	if err != nil {
		return nil, fmt.Errorf("local: start: allocating a terminal: %w", err)
	}

	cols, rows := requested.Size()

	if err := pty.Setsize(primary, &pty.Winsize{
		Rows: uint16(rows), //nolint:gosec // Terminal dimensions are small positives.
		Cols: uint16(cols), //nolint:gosec // Terminal dimensions are small positives.
	}); err != nil {
		_ = primary.Close()
		_ = secondary.Close()

		return nil, fmt.Errorf("local: start: sizing the terminal: %w", err)
	}

	cmd.Stdin = secondary
	cmd.Stdout = secondary
	cmd.Stderr = secondary
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}

	return &terminal{primary: primary, copied: make(chan struct{})}, nil
}

// start begins moving bytes once the command is running, and releases the
// parent's handle on the command's end of the terminal.
//
// The parent must let go of that end: while any handle to it remains
// open, reading the caller's end blocks forever rather than reporting
// that the command has finished with it.
func (t *terminal) start(cmd *exec.Cmd, stdio invoke.IO) {
	if secondary, ok := cmd.Stdin.(*os.File); ok {
		_ = secondary.Close()
	}

	if stdio.Stdin != nil {
		go func() {
			_, _ = io.Copy(t.primary, stdio.Stdin)
		}()
	}

	go func() {
		defer close(t.copied)

		out := stdio.Stdout
		if out == nil {
			out = io.Discard
		}

		_, _ = io.Copy(out, t.primary)
	}()
}

// finish waits for the command's output to finish arriving, then releases
// the caller's end.
//
// A terminal ends when nothing holds it open, and something the command
// left behind can hold it open indefinitely, so the wait is bounded and
// the end is released either way — which unblocks any copy still on it.
func (t *terminal) finish(grace time.Duration) {
	select {
	case <-t.copied:
	case <-time.After(grace):
	}

	_ = t.primary.Close()
}

// close releases the caller's end without waiting, for a command that
// never started.
func (t *terminal) close() {
	_ = t.primary.Close()
}
