package invoke

import "io"

// Default terminal dimensions used when a TTY is requested without an
// explicit size.
const (
	defaultTTYCols = 80
	defaultTTYRows = 24
)

// IO wires the standard streams for a single invocation of a [Command].
//
// The zero value is safe and non-interactive: the process reads immediate
// EOF from stdin and its output is discarded. Because an IO belongs to
// exactly one invocation, retried or repeated runs construct a fresh IO
// each time rather than reusing one whose streams have been consumed.
type IO struct {
	// Stdin is the process's standard input. nil means the process sees
	// immediate EOF.
	Stdin io.Reader

	// Stdout receives the process's standard output. nil discards it.
	Stdout io.Writer

	// Stderr receives the process's standard error. nil discards it.
	// Ignored when TTY is set: a pseudo-terminal merges standard error
	// into standard output.
	Stderr io.Writer

	// TTY, when non-nil, allocates a pseudo-terminal for the process.
	// Providers declare PTY support via [Capabilities]; on targets
	// without it, starting a command with TTY set fails with an error
	// wrapping [ErrNotSupported].
	TTY *TTY
}

// TTY describes the pseudo-terminal requested for an invocation.
type TTY struct {
	// Cols is the terminal width in character cells. Zero means the
	// default of 80.
	Cols int

	// Rows is the terminal height in character cells. Zero means the
	// default of 24.
	Rows int
}

// Size returns the terminal dimensions to apply, in (cols, rows) order,
// substituting the default 80x24 for unset fields.
func (t TTY) Size() (int, int) {
	cols, rows := t.Cols, t.Rows
	if cols <= 0 {
		cols = defaultTTYCols
	}

	if rows <= 0 {
		rows = defaultTTYRows
	}

	return cols, rows
}
