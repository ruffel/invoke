package fake

import (
	"context"
	"io"
	"io/fs"
	"strconv"
	"strings"
	"time"
)

// The builtin vocabulary is deliberately minimal: exactly the POSIX
// utilities the contract suite (and typical incidental shell-outs)
// exercise. Anything else is an unknown command — consumers script their
// own via Handle.

func builtinKnown(name string) bool {
	switch name {
	case "sh", "echo", "cat", "true", "false", "sleep", "printf", "test",
		"find", "mkdir", "touch", "rm", "pwd", "dd", "uname":
		return true
	default:
		return false
	}
}

// dispatch runs one command (top-level argv or a shell word) and returns
// its exit code plus whether it was interrupted by cancellation.
//
//nolint:cyclop // A flat dispatch table over the builtin vocabulary.
func dispatch(ctx context.Context, s *session, name string, args []string) (int, bool) {
	switch name {
	case "sh":
		return runShellArgv(ctx, s, args)
	case "sleep":
		return runSleep(ctx, s, args)
	case "cat":
		return runCat(ctx, s)
	case "echo":
		return writeLine(s, strings.Join(args, " "))
	case "printf":
		return runPrintf(s, args)
	case "true":
		return 0, false
	case "false":
		return 1, false
	case "test":
		return runTest(s, args), false
	case "find":
		return runFind(s, args), false
	case "mkdir":
		return runMkdir(s, args), false
	case "touch":
		return runTouch(s, args), false
	case "rm":
		return runRm(s, args), false
	case "pwd":
		return writeLine(s, s.dir)
	case "dd":
		return runDD(ctx, s, args)
	case "uname":
		// The fake simulates a Linux target (see Environment.OS); report
		// it so the os-matches-target contract can verify that claim.
		return writeLine(s, "Linux")
	default:
		_, _ = io.WriteString(s.stderr, "sh: "+name+": command not found\n")

		return exitCommandNotFound, false
	}
}

// exitCommandNotFound is the shell's conventional status for an unknown
// command.
const exitCommandNotFound = 127

func writeLine(s *session, line string) (int, bool) {
	_, _ = io.WriteString(s.stdout, line+"\n")

	return 0, false
}

func runShellArgv(ctx context.Context, s *session, args []string) (int, bool) {
	if len(args) >= 2 && args[0] == "-c" {
		return execScript(ctx, s, args[1])
	}

	_, _ = io.WriteString(s.stderr, "sh: only -c scripts are simulated\n")

	return 2, false
}

func runSleep(ctx context.Context, s *session, args []string) (int, bool) {
	if len(args) != 1 {
		_, _ = io.WriteString(s.stderr, "sleep: missing operand\n")

		return 1, false
	}

	seconds, err := strconv.ParseFloat(args[0], 64)
	if err != nil {
		_, _ = io.WriteString(s.stderr, "sleep: invalid interval\n")

		return 1, false
	}

	timer := time.NewTimer(time.Duration(seconds * float64(time.Second)))
	defer timer.Stop()

	select {
	case <-timer.C:
		return 0, false
	case <-ctx.Done():
		return -1, true
	}
}

// copyChunkBytes bounds each stdin copy step so cancellation stays
// responsive.
const copyChunkBytes = 32 * 1024

func runCat(ctx context.Context, s *session) (int, bool) {
	buf := make([]byte, copyChunkBytes)

	for {
		if ctx.Err() != nil {
			return -1, true
		}

		n, err := s.stdin.Read(buf)
		if n > 0 {
			if _, werr := s.stdout.Write(buf[:n]); werr != nil {
				return 1, false
			}
		}

		if err != nil {
			if err == io.EOF {
				return 0, false
			}

			return 1, false
		}
	}
}

func runPrintf(s *session, args []string) (int, bool) {
	if len(args) == 0 {
		return 1, false
	}

	format, values := args[0], args[1:]

	var out strings.Builder

	// POSIX printf reuses the format string until the argument list is
	// exhausted, then applies it once more if it never consumed an
	// argument (the no-conversion case).
	next := 0
	for {
		consumedBefore := next
		next = applyPrintfFormat(&out, format, values, next)

		if next >= len(values) || next == consumedBefore {
			break
		}
	}

	_, _ = io.WriteString(s.stdout, out.String())

	return 0, false
}

// applyPrintfFormat writes one pass of format, consuming %s conversions
// from values starting at next, and returns the new value index.
func applyPrintfFormat(out *strings.Builder, format string, values []string, next int) int {
	for i := 0; i < len(format); i++ {
		if format[i] == '%' && i+1 < len(format) {
			switch format[i+1] {
			case 's':
				if next < len(values) {
					out.WriteString(values[next])
					next++
				}

				i++

				continue
			case '%':
				out.WriteByte('%')

				i++

				continue
			}
		}

		out.WriteByte(format[i])
	}

	return next
}

func runTest(s *session, args []string) int {
	negate := false
	if len(args) > 0 && args[0] == "!" {
		negate = true
		args = args[1:]
	}

	ok := evalTest(s, args)
	if negate {
		ok = !ok
	}

	if ok {
		return 0
	}

	return 1
}

func evalTest(s *session, args []string) bool {
	if len(args) != 2 {
		return false
	}

	path := vfsClean(s.dir, args[1])

	node, exists := s.fs.snapshot(path)

	switch args[0] {
	case "-e":
		return exists
	case "-d":
		return exists && node.dir
	case "-f":
		// A regular file: present, and neither a directory nor a link.
		// The fake does not follow links here, which suffices for the
		// contracts that probe already-materialized regular files.
		return exists && !node.dir && node.link == ""
	case "-L":
		return exists && node.link != ""
	case "-n":
		return args[1] != ""
	case "-t":
		return false // The fake never allocates a terminal.
	default:
		return false
	}
}

func runFind(s *session, args []string) int {
	// The simulated form: find PATH -maxdepth 0 -perm MODE.
	if len(args) != 5 || args[1] != "-maxdepth" || args[2] != "0" || args[3] != "-perm" {
		_, _ = io.WriteString(s.stderr, "find: only 'PATH -maxdepth 0 -perm MODE' is simulated\n")

		return 2
	}

	target := vfsClean(s.dir, args[0])

	node, ok := s.fs.snapshot(target)
	if !ok {
		_, _ = io.WriteString(s.stderr, "find: "+args[0]+": no such file or directory\n")

		return 1
	}

	const octalBase = 8

	want, err := strconv.ParseUint(args[4], octalBase, 32)
	if err != nil {
		return 2
	}

	if node.mode.Perm() == fs.FileMode(want) {
		_, _ = io.WriteString(s.stdout, args[0]+"\n")
	}

	return 0
}

func runMkdir(s *session, args []string) int {
	if len(args) > 0 && args[0] == "-p" {
		args = args[1:]
	}

	for _, arg := range args {
		if err := s.fs.mkdirAll(vfsClean(s.dir, arg)); err != nil {
			_, _ = io.WriteString(s.stderr, "mkdir: "+err.Error()+"\n")

			return 1
		}
	}

	return 0
}

func runTouch(s *session, args []string) int {
	for _, arg := range args {
		if err := s.fs.touch(vfsClean(s.dir, arg)); err != nil {
			_, _ = io.WriteString(s.stderr, "touch: "+err.Error()+"\n")

			return 1
		}
	}

	return 0
}

func runRm(s *session, args []string) int {
	if len(args) > 0 && (args[0] == "-rf" || args[0] == "-fr" || args[0] == "-r" || args[0] == "-f") {
		args = args[1:]
	}

	for _, arg := range args {
		s.fs.removeAll(vfsClean(s.dir, arg))
	}

	return 0
}

func runDD(ctx context.Context, s *session, args []string) (int, bool) {
	var input string

	blockSize, count := 512, 1

	for _, arg := range args {
		key, value, _ := strings.Cut(arg, "=")

		switch key {
		case "if":
			input = value
		case "bs":
			if n, err := strconv.Atoi(value); err == nil {
				blockSize = n
			}
		case "count":
			if n, err := strconv.Atoi(value); err == nil {
				count = n
			}
		}
	}

	if input != "/dev/zero" {
		_, _ = io.WriteString(s.stderr, "dd: only if=/dev/zero is simulated\n")

		return 1, false
	}

	zeros := make([]byte, blockSize)

	for range count {
		if ctx.Err() != nil {
			return -1, true
		}

		if _, err := s.stdout.Write(zeros); err != nil {
			return 1, false
		}
	}

	_, _ = io.WriteString(s.stderr, strconv.Itoa(count)+"+0 records in\n")

	return 0, false
}
