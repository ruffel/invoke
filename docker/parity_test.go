//go:build docker

package docker_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/docker"
	"github.com/ruffel/invoke/local"
	"github.com/ruffel/invoke/ssh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// observation is what a scenario reports: the semantics a caller can
// actually observe, not the wording a provider chose. Error messages name
// their transport and are expected to differ; what a caller branches on
// must not.
type observation struct {
	Stdout   string
	ExitCode int
	Outcome  string
}

// classify reduces an error to the outcome a caller can branch on.
func classify(err error) string {
	var (
		exitErr      *invoke.ExitError
		transportErr *invoke.TransportError
	)

	switch {
	case err == nil:
		return "ok"
	case errors.As(err, &exitErr):
		return "exit"
	case errors.As(err, &transportErr):
		return "transport"
	case errors.Is(err, invoke.ErrNotFound):
		return "not-found"
	case errors.Is(err, invoke.ErrInvalidWorkdir):
		return "invalid-workdir"
	case errors.Is(err, invoke.ErrNotSupported):
		return "not-supported"
	case errors.Is(err, invoke.ErrClosed):
		return "closed"
	default:
		return "other"
	}
}

// scenario is one comparison, run identically against every provider.
type scenario struct {
	name string

	// setup places whatever the scenario needs on the target, using the
	// provider's own transfer so every target is prepared the same way.
	setup func(t *testing.T, env invoke.Environment, dir string)

	// command builds the command to run, given the directory setup used.
	command func(dir string) invoke.Command

	// stdin is delivered to the command when non-empty.
	stdin string
}

func parityScenarios() []scenario {
	return []scenario{
		{
			// The one that started this: a relative executable must
			// resolve against the working directory the command runs in.
			// SSH resolved it against the login directory instead, which
			// no contract phrased as a rule.
			name: "relative path resolves against the working directory",
			setup: func(t *testing.T, env invoke.Environment, dir string) {
				t.Helper()

				uploadScript(t, env, dir+"/script.sh", "#!/bin/sh\necho ran\n")
			},
			command: func(dir string) invoke.Command {
				cmd := invoke.New("./script.sh")
				cmd.Dir = dir

				return cmd
			},
		},
		{
			name: "an empty environment value arrives set, not unset",
			command: func(string) invoke.Command {
				cmd := invoke.Shell(`printf '[%s]' "${EMPTY-unset}"`)
				cmd.Env = []string{"EMPTY="}

				return cmd
			},
		},
		{
			name: "an environment value keeps everything after the first equals",
			command: func(string) invoke.Command {
				cmd := invoke.New("printenv", "PAIR")
				cmd.Env = []string{"PAIR=a=b=c"}

				return cmd
			},
		},
		{
			name: "an empty argument survives as an argument",
			command: func(string) invoke.Command {
				return invoke.New("printf", "[%s]", "")
			},
		},
		{
			name: "shell metacharacters in an argument stay data",
			command: func(string) invoke.Command {
				return invoke.New("printf", "%s", "$(id); `id`; rm -rf /")
			},
		},
		{
			name: "a newline inside an argument survives",
			command: func(string) invoke.Command {
				return invoke.New("printf", "%s", "one\ntwo")
			},
		},
		{
			name: "an exit status above the signal boundary stays an exit status",
			command: func(string) invoke.Command {
				return invoke.Shell("exit 137")
			},
		},
		{
			name: "reading an unset variable fails the same way",
			command: func(string) invoke.Command {
				return invoke.Shell("printenv DEFINITELY_NOT_SET_9f3a")
			},
		},
		{
			name: "input without a trailing newline is delivered whole",
			command: func(string) invoke.Command {
				return invoke.New("cat")
			},
			stdin: "no trailing newline",
		},
		{
			name: "a working directory reached through a link is usable",
			setup: func(t *testing.T, env invoke.Environment, dir string) {
				t.Helper()

				uploadScript(t, env, dir+"/real/marker", "present\n")
			},
			command: func(dir string) invoke.Command {
				cmd := invoke.Shell("cat marker")
				cmd.Dir = dir + "/real"

				return cmd
			},
		},
	}
}

// TestProviderParity runs every scenario against every provider and
// requires them to agree.
//
// This is what the contract suite cannot do: a contract asks whether each
// provider obeys a stated rule, one provider at a time. Divergence in
// anything no rule names — how a relative path resolves, whether an empty
// environment value arrives set or unset — passes every contract and
// still means a command behaves differently depending on where it runs.
//
// It lives in this module because comparing all the providers means
// importing the container one, which the core module cannot.
//
//nolint:paralleltest,tparallel // Subtests share the providers deliberately; see below.
func TestProviderParity(t *testing.T) {
	t.Parallel()

	providers := startProviders(t)

	// Sequential on purpose: the providers are shared, and a server caps
	// the sessions one connection may carry at once. Running the
	// comparisons concurrently would measure that cap rather than
	// whether the providers agree.
	for _, sc := range parityScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			observed := make(map[string]observation, len(providers))

			for name, env := range providers {
				observed[name] = observe(t, env, sc)
			}

			// The local provider is the reference implementation; the
			// others are compared to it rather than to each other, so a
			// failure names which one drifted.
			reference := observed["local"]

			for name, got := range observed {
				if name == "local" {
					continue
				}

				assert.Equal(t, reference, got,
					"%s diverges from local on %q", name, sc.name)
			}
		})
	}
}

// observe runs one scenario against one provider.
func observe(t *testing.T, env invoke.Environment, sc scenario) observation {
	t.Helper()

	dir := "/tmp/invoke-parity-" + strings.ReplaceAll(t.Name(), "/", "-")

	requireRun(t, env, invoke.New("rm", "-rf", dir))
	requireRun(t, env, invoke.New("mkdir", "-p", dir+"/real"))

	if sc.setup != nil {
		sc.setup(t, env, dir)
	}

	var out strings.Builder

	stdio := invoke.IO{Stdout: &out}
	if sc.stdin != "" {
		stdio.Stdin = strings.NewReader(sc.stdin)
	}

	proc, err := env.Start(t.Context(), sc.command(dir), stdio)
	if err != nil {
		return observation{Outcome: classify(err), ExitCode: -1}
	}

	result, waitErr := proc.Wait()

	return observation{
		Stdout:   out.String(),
		ExitCode: result.ExitCode,
		Outcome:  classify(waitErr),
	}
}

// requireRun runs a preparation command and fails the test if it does not
// succeed, so a scenario never compares results built on a broken setup.
func requireRun(t *testing.T, env invoke.Environment, cmd invoke.Command) {
	t.Helper()

	proc, err := env.Start(t.Context(), cmd, invoke.IO{})
	require.NoError(t, err, "preparing the target")

	_, waitErr := proc.Wait()
	require.NoError(t, waitErr, "preparing the target")
}

// uploadScript places an executable file on the target through the
// provider's own transfer, so every target is prepared identically.
func uploadScript(t *testing.T, env invoke.Environment, remote, content string) {
	t.Helper()

	local := filepath.Join(t.TempDir(), filepath.Base(remote))
	require.NoError(t, os.WriteFile(local, []byte(content), 0o700))

	require.NoError(t, env.Upload(t.Context(), local, remote, invoke.WithMode(0o700)),
		"placing %q on the target", remote)
}

// startProviders brings up one environment per provider.
func startProviders(t *testing.T) map[string]invoke.Environment {
	t.Helper()

	localEnv, err := local.New()
	require.NoError(t, err, "local.New")

	t.Cleanup(func() { _ = localEnv.Close() })

	id := startContainer(t)

	dockerEnv, err := docker.New(id, docker.WithHost(daemonHost(t)))
	require.NoError(t, err, "docker.New")

	t.Cleanup(func() { _ = dockerEnv.Close() })

	sshEnv := startParitySSHD(t)

	return map[string]invoke.Environment{
		"local":  localEnv,
		"ssh":    sshEnv,
		"docker": dockerEnv,
	}
}

// startParitySSHD runs a real sshd in a container and connects to it, so
// the SSH side of the comparison is a real server rather than a stand-in.
func startParitySSHD(t *testing.T) invoke.Environment {
	t.Helper()

	const (
		user     = "root"
		password = "testpass"
	)

	setup := "apk add --no-cache openssh >/dev/null 2>&1 && ssh-keygen -A >/dev/null 2>&1 && " +
		"echo '" + user + ":" + password + "' | chpasswd && " +
		"sed -i 's/^#*PermitRootLogin.*/PermitRootLogin yes/' /etc/ssh/sshd_config && /usr/sbin/sshd -D"

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()

	out, err := exec.CommandContext(ctx, "docker", "run", "-d", "--rm",
		"-p", "127.0.0.1::22", testImage, "sh", "-c", setup).Output()
	require.NoError(t, err, "starting the sshd container")

	id := strings.TrimSpace(string(out))

	t.Cleanup(func() {
		removeCtx, removeCancel := context.WithTimeout(context.Background(), time.Minute)
		defer removeCancel()

		//nolint:gosec // The argument is a container id this function just created.
		_ = exec.CommandContext(removeCtx, "docker", "rm", "-f", id).Run()
	})

	port := parityPort(t, id)

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		env, dialErr := ssh.New("127.0.0.1",
			ssh.WithPort(port), ssh.WithUser(user), ssh.WithPassword(password),
			ssh.WithInsecureIgnoreHostKey())
		if dialErr == nil {
			t.Cleanup(func() { _ = env.Close() })

			return env
		}

		time.Sleep(500 * time.Millisecond)
	}

	require.FailNow(t, "sshd did not become reachable")

	return nil
}

// parityPort resolves the port the sshd container published.
func parityPort(t *testing.T, id string) int {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.CommandContext(t.Context(), "docker", "port", id, "22/tcp").Output()
		if err == nil {
			mapped := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
			if idx := strings.LastIndex(mapped, ":"); idx >= 0 {
				if port, convErr := strconv.Atoi(mapped[idx+1:]); convErr == nil {
					return port
				}
			}
		}

		time.Sleep(250 * time.Millisecond)
	}

	require.FailNow(t, "the container never published its ssh port")

	return 0
}
