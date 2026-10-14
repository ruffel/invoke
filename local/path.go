package local

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// resolveIn finds a bare command name through the PATH list the caller
// set in Command.Env, as the command's own environment would.
//
// os/exec resolves a name when the command is built, against this
// process's PATH, before the caller's environment exists, so a PATH in
// Command.Env reached the command without having found it. Each entry is
// asked through exec.LookPath, which keeps the platform's own rules for
// what is executable, PATHEXT included.
//
// Relative entries are skipped. os/exec refuses to run what one finds,
// for the same reason: a command found in the working directory is
// whatever happens to be there.
func resolveIn(name, pathList string) (string, error) {
	for _, dir := range filepath.SplitList(pathList) {
		if !filepath.IsAbs(dir) {
			continue
		}

		if found, err := exec.LookPath(filepath.Join(dir, name)); err == nil {
			return found, nil
		}
	}

	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

// callerPath returns the PATH Command.Env sets, last one winning as it
// does in the environment the command receives. Windows names variables
// without regard to case.
func callerPath(env []string) (string, bool) {
	value, found := "", false

	for _, pair := range env {
		key, v, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}

		if key == "PATH" || (runtime.GOOS == "windows" && strings.EqualFold(key, "PATH")) {
			value, found = v, true
		}
	}

	return value, found
}
