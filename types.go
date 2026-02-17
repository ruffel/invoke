package invoke

import (
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"

	"github.com/google/shlex"
)

// Command configures a process execution.
type Command struct {
	Cmd  string   // Binary name or path to executable
	Args []string // Arguments to pass to the binary
	Env  []string // Environment variables in "KEY=VALUE" format
	Dir  string   // Working directory for execution

	// Standard streams. If nil, defaults to empty/discard.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// Tty allocates a PTY. Useful for interactive commands (e.g. sudo).
	Tty bool
}

// Validate checks that the command is well-formed.
// Returns an error if the command is nil or has an empty binary.
func (c *Command) Validate() error {
	if c == nil {
		return errors.New("command cannot be nil")
	}

	if strings.TrimSpace(c.Cmd) == "" {
		return errors.New("command binary cannot be empty")
	}

	return nil
}

// NewCommand creates a new Command with the given binary and arguments.
func NewCommand(binary string, args ...string) *Command {
	return &Command{
		Cmd:  binary,
		Args: args,
	}
}

// String returns a simplified, shell-quoted string representation of the command.
func (c *Command) String() string {
	if len(c.Args) == 0 {
		return c.Cmd
	}

	var b strings.Builder
	b.WriteString(c.Cmd)

	for _, arg := range c.Args {
		b.WriteString(" ")

		if strings.Contains(arg, " ") {
			fmt.Fprintf(&b, "%q", arg)
		} else {
			b.WriteString(arg)
		}
	}

	return b.String()
}

// ParseCommand parses a shell command string into a Command struct using shlex.
// It handles quoted arguments correctly.
func ParseCommand(cmdStr string) (*Command, error) {
	parts, err := shlex.Split(cmdStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse command: %w", err)
	}

	if len(parts) == 0 {
		return nil, errors.New("empty command")
	}

	return &Command{
		Cmd:  parts[0],
		Args: parts[1:],
	}, nil
}

// Result contains metadata about a completed command execution.
type Result struct {
	ExitCode int           // Process exit code (0 indicates success)
	Duration time.Duration // Time taken for execution
	Error    error         // Launch/Transport error (distinct from non-zero exit code)
}

// BufferedResult extends Result to include captured stdout/stderr content.
// Returned by Executor.RunBuffered.
type BufferedResult struct {
	Result

	Stdout []byte
	Stderr []byte
}

// Success returns true if the command completed with exit code 0 and no transport error.
func (r *Result) Success() bool {
	return r.ExitCode == 0 && r.Error == nil
}

// Failed returns true if the command failed (non-zero exit code or transport error).
func (r *Result) Failed() bool {
	return !r.Success()
}

// TargetOS identifies the operating system of the target environment.
type TargetOS int

const (
	// OSUnknown represents an unidentified operating system.
	OSUnknown TargetOS = iota
	// OSLinux represents the Linux kernel.
	OSLinux
	// OSWindows represents Microsoft Windows.
	OSWindows
	// OSDarwin represents macOS (Darwin).
	OSDarwin
)

func (os TargetOS) String() string {
	switch os {
	case OSLinux:
		return "linux"
	case OSWindows:
		return "windows"
	case OSDarwin:
		return "darwin"
	case OSUnknown:
		return "unknown"
	default:
		return "unknown"
	}
}

// ShellCommand constructs a command that runs the provided script inside the system shell.
// Returns "sh -c <script>" for UNIX-likes and "powershell ..." for Windows.
func (os TargetOS) ShellCommand(script string) *Command {
	switch os {
	case OSWindows:
		// Default to PowerShell for Windows as it's the modern standard
		return &Command{
			Cmd:  "powershell",
			Args: []string{"-NoProfile", "-NonInteractive", "-Command", script},
		}
	case OSLinux, OSDarwin, OSUnknown:
		fallthrough
	default: // Linux, Darwin, Unknown
		return &Command{
			Cmd:  "sh",
			Args: []string{"-c", script},
		}
	}
}

// ParseTargetOS converts a typical OS string (e.g., "linux", "darwin") to a TargetOS.
func ParseTargetOS(osStr string) TargetOS {
	switch strings.ToLower(strings.TrimSpace(osStr)) {
	case "linux":
		return OSLinux
	case "windows", "windows_nt":
		return OSWindows
	case "darwin", "macos":
		return OSDarwin
	default:
		return OSUnknown
	}
}

// DetectLocalOS returns the TargetOS of the current running process.
func DetectLocalOS() TargetOS {
	return ParseTargetOS(runtime.GOOS)
}
