//go:build docker

package docker_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/ruffel/invoke/docker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWithUserRunsCommandsAsThatUser checks commands run as the
// configured user, in each form the option accepts.
//
// A user that never reaches the daemon fails nothing: the command runs
// as the container's default, which in most images is root. The option
// being lost on the way therefore looks like success, with more
// privilege than was asked for.
func TestWithUserRunsCommandsAsThatUser(t *testing.T) {
	t.Parallel()

	id := startContainer(t)

	cases := []struct {
		name string
		user string
		want string
	}{
		{name: "the container's default", user: "", want: "0:0"},
		{name: "an account name", user: "nobody", want: "65534:65534"},
		{name: "a numeric user and group", user: "1234:5678", want: "1234:5678"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env, err := docker.New(t.Context(), id, docker.WithUser(tc.user))
			require.NoError(t, err, "docker.New")

			t.Cleanup(func() { _ = env.Close() })

			out, err := runInContainer(t, env, "sh", "-c", `echo "$(id -u):$(id -g)"`)
			require.NoError(t, err, "reading the command's identity")

			assert.Equal(t, tc.want, strings.TrimSpace(out),
				"the command ran as a different user than the one configured")
		})
	}
}

// TestWithPrivilegedWidensCapabilities checks a privileged command holds
// capabilities an ordinary command in the same container does not.
//
// Like the user, a privilege flag that never reaches the daemon fails
// nothing by itself; the command simply runs without it, and what it
// needed the privilege for fails later and somewhere else.
func TestWithPrivilegedWidensCapabilities(t *testing.T) {
	t.Parallel()

	id := startContainer(t)

	ordinary := effectiveCapabilities(t, id)
	privileged := effectiveCapabilities(t, id, docker.WithPrivileged())

	assert.Equal(t, ordinary, ordinary&privileged,
		"a privileged command must keep every capability an ordinary one holds")
	assert.NotZero(t, privileged&^ordinary,
		"a privileged command must hold capabilities an ordinary one does not")
}

// effectiveCapabilities returns the capability set a command runs with
// under the given options, as the bit mask the kernel reports.
func effectiveCapabilities(t *testing.T, id string, opts ...docker.Option) uint64 {
	t.Helper()

	env, err := docker.New(t.Context(), id, opts...)
	require.NoError(t, err, "docker.New")

	defer func() { _ = env.Close() }()

	// The line reads "CapEff:" followed by the mask in hexadecimal.
	out, err := runInContainer(t, env, "grep", "CapEff", "/proc/self/status")
	require.NoError(t, err, "reading the command's capabilities")

	fields := strings.Fields(out)
	require.Len(t, fields, 2, "unexpected capability line %q", out)

	mask, err := strconv.ParseUint(fields[1], 16, 64)
	require.NoError(t, err, "parsing the capability mask %q", fields[1])

	return mask
}
