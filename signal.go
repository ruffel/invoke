package invoke

// Signal names a POSIX signal to deliver to a process, in a form that maps
// cleanly onto every target: syscall numbers locally, SSH signal names on
// the wire, and kill -s in containers.
//
// Providers declare working signal delivery via [Capabilities]. Delivering
// a signal a target cannot support returns an error wrapping
// [ErrNotSupported] — never a silent no-op.
type Signal string

// The supported signal set. Window-size changes (SIGWINCH) and terminal
// resize forwarding are out of scope this cycle.
const (
	// SIGINT interrupts the process, as Ctrl-C does.
	SIGINT Signal = "INT"

	// SIGTERM requests graceful termination.
	SIGTERM Signal = "TERM"

	// SIGKILL forcibly terminates the process; it cannot be caught.
	SIGKILL Signal = "KILL"

	// SIGHUP reports that the controlling terminal hung up.
	SIGHUP Signal = "HUP"

	// SIGQUIT requests termination with a core dump.
	SIGQUIT Signal = "QUIT"

	// SIGUSR1 is the first user-defined signal.
	SIGUSR1 Signal = "USR1"

	// SIGUSR2 is the second user-defined signal.
	SIGUSR2 Signal = "USR2"
)
