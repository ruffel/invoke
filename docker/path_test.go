//go:build docker

package docker_test

import (
	"testing"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/require"
)

// TestCallerPathWithoutAShellStillStartsTheCommand checks a command found
// only through the caller's PATH starts when that PATH has no shell on it.
//
// Every command runs under a small sh wrapper that records its id for
// signalling, and the daemon resolves the wrapper's name through the
// exec's environment, which is the caller's. A PATH naming one tool
// directory let the pre-flight approve the command and then left the
// daemon unable to find sh to launch it.
func TestCallerPathWithoutAShellStillStartsTheCommand(t *testing.T) {
	t.Parallel()

	env := dialContainer(t)

	_, err := runInContainer(t, env, "mkdir", "-p", "/opt/tools")
	require.NoError(t, err, "making the tool directory")

	_, err = runInContainer(t, env, "ln", "-s", "/bin/true", "/opt/tools/true")
	require.NoError(t, err, "linking the one tool")

	cmd := invoke.New("true")
	cmd.Env = []string{"PATH=/opt/tools"}

	_, err = invoke.NewExecutor(env).Run(t.Context(), cmd, invoke.IO{})
	require.NoError(t, err, "true is on the caller's PATH, which has no shell on it")
}
