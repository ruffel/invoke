package ssh

import (
	"context"
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ruffel/invoke"
	"golang.org/x/crypto/ssh"
)

// How a command's environment reaches it.
//
// Variables go out of band first, where they never appear in the remote
// process table. A server accepts only those its AcceptEnv setting names,
// though, and the stock setting names none — so refusal is the ordinary
// case rather than the exception, and running the command without its
// environment is not an option.
//
// Refused variables are therefore written to a file only the login user
// can read, which the command line sources and deletes before running the
// command. The values never reach an argument vector. A caller who cannot
// use a file — a read-only target, say — can opt into carrying them on the
// command line instead, where every account on the host can read them.

// envDeliveryFailed is the exit status reporting that the environment
// file could not be read, so the command was not run.
//
// The status is reserved on the file-delivery route: unlike the pre-check
// statuses, which run in an exec of their own, this one shares the
// command's session, so a command of its own exiting 93 under that route
// is read as a delivery failure. WithCommandLineEnv avoids the file and
// with it the reservation.
const envDeliveryFailed = 93

// cleanupTimeout bounds the best-effort removal of a delivery file.
const cleanupTimeout = 10 * time.Second

// deliverEnv arranges for env to reach the command, returning any
// prologue the command line needs to carry and the path of the delivery
// file when the file route is in use.
func (e *Environment) deliverEnv(ctx context.Context, session *ssh.Session, env []string) (string, string, error) {
	var refused []string

	for _, pair := range env {
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}

		if err := session.Setenv(key, value); err != nil {
			refused = append(refused, pair)
		}
	}

	if len(refused) == 0 {
		return "", "", nil
	}

	if e.cfg.CommandLineEnv {
		return exportPrologue(refused), "", nil
	}

	// A name no concurrent command, and no attacker, can predict: a
	// predictable name is one that can be created first, so the file
	// would be written to something somebody else owns.
	path := "/tmp/.invoke-env-" + rand.Text()

	if err := e.writeRemoteFile(ctx, path, exportScript(refused)); err != nil {
		return "", "", fmt.Errorf(
			"ssh: start: the server refused %s and they could not be delivered by file either; "+
				"pass WithCommandLineEnv to send them on the command line, where every account "+
				"on the host can read them: %w",
			strings.Join(refusedNames(refused), ", "), err)
	}

	return sourcePrologue(path), path, nil
}

// writeRemoteFile creates a file readable only by the login user, with
// its content carried on the command's input rather than its arguments.
func (e *Environment) writeRemoteFile(ctx context.Context, path, content string) error {
	session, err := e.client.NewSession()
	if err != nil {
		return &invoke.TransportError{Op: "session", Err: err}
	}

	defer func() { _ = session.Close() }()

	session.Stdin = strings.NewReader(content)

	done := make(chan error, 1)

	go func() {
		done <- session.Run("umask 077; cat > " + quoteArg(path))
	}()

	select {
	case <-ctx.Done():
		_ = session.Close()

		<-done

		return ctx.Err()

	case runErr := <-done:
		if runErr != nil {
			return &invoke.TransportError{Op: "write", Err: runErr}
		}

		return nil
	}
}

// removeRemoteFile deletes a file this package created, best effort and
// detached from any caller context: the outcome the file supported has
// already been decided, and a secret left in /tmp is worth one more
// round trip.
func (e *Environment) removeRemoteFile(path string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), cleanupTimeout)
	defer cancel()

	if _, _, err := e.runRaw(ctx, "rm -f "+quoteArg(path)); err != nil {
		return // Best effort: the outcome the file supported is already decided.
	}
}

// refusedNames lists the variable names from KEY=VALUE pairs, so an error
// can name them without quoting their values.
func refusedNames(pairs []string) []string {
	names := make([]string, 0, len(pairs))

	for _, pair := range pairs {
		key, _, _ := strings.Cut(pair, "=")
		names = append(names, key)
	}

	return names
}

// exportScript renders KEY=VALUE pairs as a shell script that exports
// them, for a file the command line sources.
func exportScript(pairs []string) string {
	var script strings.Builder

	for _, pair := range pairs {
		if key, value, ok := strings.Cut(pair, "="); ok {
			script.WriteString("export " + key + "=" + quoteArg(value) + "\n")
		}
	}

	return script.String()
}

// exportPrologue renders the pairs directly onto the command line, where
// they are visible to every account on the remote host.
func exportPrologue(pairs []string) string {
	var prologue strings.Builder

	for _, pair := range pairs {
		if key, value, ok := strings.Cut(pair, "="); ok {
			prologue.WriteString("export " + key + "=" + quoteArg(value) + "; ")
		}
	}

	return prologue.String()
}

// sourcePrologue reads the environment from a file and removes it before
// the command runs, so it exists only for the moment in between.
//
// Failing to read it exits rather than running the command without its
// environment, which is the outcome this whole route exists to avoid.
// The readability test stands on its own ahead of the source: dot is a
// special built-in, and a POSIX shell aborts outright when one fails,
// skipping any || that was waiting to catch it — the guard would never
// fire from where it looks like it belongs.
//
// The file is removed under the session's own PATH rather than the one
// it just set. A caller's PATH that names only their own tools has no rm
// on it, and the file, secrets and all, stayed where it was. Saving and
// restoring PATH costs no process, unlike asking where rm is, and
// unlike command -p it finds rm wherever this host keeps it.
func sourcePrologue(path string) string {
	quoted := quoteArg(path)

	return "[ -r " + quoted + " ] || exit " + strconv.Itoa(envDeliveryFailed) + "; " +
		"__invoke_path=$PATH; . " + quoted + "; " +
		"__invoke_caller_path=$PATH; PATH=$__invoke_path; rm -f " + quoted + "; " +
		"PATH=$__invoke_caller_path; unset __invoke_path __invoke_caller_path; "
}
