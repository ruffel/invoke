package docker

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeContext creates a docker configuration directory holding one
// context, and points the process at it.
func writeContext(t *testing.T, current, name, host string) {
	t.Helper()

	dir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dir)
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")

	if current != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"),
			[]byte(`{"currentContext":`+quote(current)+`}`), 0o600))
	}

	if name == "" {
		return
	}

	digest := sha256.Sum256([]byte(name))
	metaDir := filepath.Join(dir, "contexts", "meta", hex.EncodeToString(digest[:]))
	require.NoError(t, os.MkdirAll(metaDir, 0o750))

	meta := `{"Name":` + quote(name) + `,"Endpoints":{"docker":{"Host":` + quote(host) + `}}}`
	require.NoError(t, os.WriteFile(filepath.Join(metaDir, "meta.json"), []byte(meta), 0o600))
}

func quote(s string) string { return `"` + s + `"` }

// TestResolveHostReadsTheCurrentContext checks the endpoint comes from the
// context the installation selects, which is where anything not using the
// conventional socket records it.
//
//nolint:paralleltest // t.Setenv, which these need, forbids t.Parallel.
func TestResolveHostReadsTheCurrentContext(t *testing.T) {
	writeContext(t, "colima", "colima", "unix:///home/u/.colima/docker.sock")

	host, err := resolveHost(&Config{})
	require.NoError(t, err)

	assert.Equal(t, "unix:///home/u/.colima/docker.sock", host)
}

// TestResolveHostPrefersExplicitSettings checks the order the docker
// command itself uses: an endpoint given directly wins, then the
// environment, then the context.
//
//nolint:paralleltest // t.Setenv, which these need, forbids t.Parallel.
func TestResolveHostPrefersExplicitSettings(t *testing.T) {
	//nolint:paralleltest // t.Setenv forbids t.Parallel.
	t.Run("configured endpoint wins", func(t *testing.T) {
		writeContext(t, "colima", "colima", "unix:///from/context.sock")

		host, err := resolveHost(&Config{Host: "tcp://chosen:2375"})
		require.NoError(t, err)

		assert.Equal(t, "tcp://chosen:2375", host)
	})

	//nolint:paralleltest // t.Setenv forbids t.Parallel.
	t.Run("environment beats the context", func(t *testing.T) {
		writeContext(t, "colima", "colima", "unix:///from/context.sock")
		t.Setenv("DOCKER_HOST", "tcp://from-env:2375")

		host, err := resolveHost(&Config{})
		require.NoError(t, err)

		// Empty defers to the client, which reads DOCKER_HOST itself.
		assert.Empty(t, host, "DOCKER_HOST is the client's to apply")
	})

	//nolint:paralleltest // t.Setenv forbids t.Parallel.
	t.Run("a named context beats the current one", func(t *testing.T) {
		writeContext(t, "colima", "other", "unix:///from/other.sock")

		host, err := resolveHost(&Config{Context: "other"})
		require.NoError(t, err)

		assert.Equal(t, "unix:///from/other.sock", host)
	})
}

// TestResolveHostDefersWhenNothingIsConfigured checks an installation
// using the conventional socket is left alone.
//
//nolint:paralleltest // t.Setenv, which these need, forbids t.Parallel.
func TestResolveHostDefersWhenNothingIsConfigured(t *testing.T) {
	//nolint:paralleltest // t.Setenv forbids t.Parallel.
	t.Run("no configuration at all", func(t *testing.T) {
		writeContext(t, "", "", "")

		host, err := resolveHost(&Config{})
		require.NoError(t, err)

		assert.Empty(t, host)
	})

	//nolint:paralleltest // t.Setenv forbids t.Parallel.
	t.Run("the built-in context", func(t *testing.T) {
		writeContext(t, "default", "", "")

		host, err := resolveHost(&Config{})
		require.NoError(t, err)

		assert.Empty(t, host, "the default context means the client's own defaults")
	})
}

// TestResolveHostRefusesAContextItCannotRead checks a context named
// deliberately fails rather than falling back.
//
// Falling back would reach a different daemon, so commands would run
// somewhere the caller did not choose — the failure that is worth
// reporting rather than working around.
//
//nolint:paralleltest // t.Setenv, which these need, forbids t.Parallel.
func TestResolveHostRefusesAContextItCannotRead(t *testing.T) {
	writeContext(t, "", "", "")

	_, err := resolveHost(&Config{Context: "missing"})
	require.Error(t, err, "a context that cannot be read must not fall back")
	assert.ErrorContains(t, err, "missing", "the error must name the context")
}

// TestResolveHostReportsTLSContextsPlainly checks a context needing client
// certificates says so, rather than connecting without them and failing
// for a reason that names the wrong cause.
//
//nolint:paralleltest // t.Setenv, which these need, forbids t.Parallel.
func TestResolveHostReportsTLSContextsPlainly(t *testing.T) {
	writeContext(t, "remote", "remote", "tcp://remote:2376")

	digest := sha256.Sum256([]byte("remote"))
	tlsDir := filepath.Join(os.Getenv("DOCKER_CONFIG"), "contexts", "tls",
		hex.EncodeToString(digest[:]), "docker")
	require.NoError(t, os.MkdirAll(tlsDir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(tlsDir, "cert.pem"), []byte("x"), 0o600))

	_, err := resolveHost(&Config{})
	require.Error(t, err)
	assert.ErrorIs(t, err, invoke.ErrNotSupported)
}
