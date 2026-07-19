package ssh

import (
	"testing"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDefaultEnvRouteKeepsValuesOffTheCommandLine pins the property the
// file-based route exists for.
//
// The command line becomes the argv of a process on the remote host, and
// a process's arguments are readable by every account there. The default
// route must therefore mention only where to read the environment from,
// never what is in it.
func TestDefaultEnvRouteKeepsValuesOffTheCommandLine(t *testing.T) {
	t.Parallel()

	const secret = "s3cr3t-value"

	cmd := invoke.New("printenv", "TOKEN")

	line := commandLine(cmd, sourcePrologue("/tmp/.invoke-env-abc123"))

	assert.NotContains(t, line, secret, "the default route must not carry values on the command line")
	assert.Contains(t, line, "/tmp/.invoke-env-abc123", "the command line must say where to read them from")
	assert.Contains(t, line, "rm -f", "the file must be removed once read")

	// The values live in the file, which is created readable only by the
	// login user.
	script := exportScript([]string{"TOKEN=" + secret})
	assert.Contains(t, script, secret, "the file is what carries the values")
	assert.Contains(t, script, "export TOKEN=", "they must be exported to survive the exec")
}

// TestCommandLineEnvRouteCarriesValuesAndSaysSo pins the opt-in route's
// cost: the values really are on the command line, which is the whole
// reason it is not the default.
func TestCommandLineEnvRouteCarriesValuesAndSaysSo(t *testing.T) {
	t.Parallel()

	const secret = "s3cr3t-value"

	line := commandLine(invoke.New("printenv", "TOKEN"), exportPrologue([]string{"TOKEN=" + secret}))

	assert.Contains(t, line, secret, "the opt-in route carries values on the command line by definition")
}

// TestEnvValuesAreQuotedOnEitherRoute checks a value cannot break out of
// its assignment and become script, whichever route carries it.
func TestEnvValuesAreQuotedOnEitherRoute(t *testing.T) {
	t.Parallel()

	hostile := []string{"EVIL=x'; touch /tmp/pwned; echo '"}

	for name, rendered := range map[string]string{
		"file":         exportScript(hostile),
		"command line": exportPrologue(hostile),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			require.NotContains(t, rendered, "; touch /tmp/pwned; echo ;",
				"the value escaped its quoting and became script")

			// Everything after the first quote up to the closing one is
			// literal, so the injected text survives as data.
			assert.Contains(t, rendered, `'\''`,
				"an embedded quote must be escaped, not passed through")
		})
	}
}
