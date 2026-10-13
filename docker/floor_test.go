package docker_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/moby/moby/client"
	"github.com/ruffel/invoke/docker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// documentedFloor is the oldest Engine API version the README and the
// package documentation say the provider supports: the one Docker 19.03
// introduced.
const documentedFloor = "1.40"

// TestDaemonFloorIsTheDocumentedOne pins the oldest daemon the provider
// can talk to at the version its documentation promises.
//
// The floor is not this package's to choose. It is the lowest API version
// the client library will speak, and the library moves it between
// releases. Raising it puts working daemons out of reach with no change
// to this package's API and nothing a compiler would notice, so an
// upgrade that moves it has to fail here — where it prompts a line in the
// release notes — rather than in somebody's deployment.
func TestDaemonFloorIsTheDocumentedOne(t *testing.T) {
	t.Parallel()

	assert.Equal(t, documentedFloor, client.MinAPIVersion,
		"the client library's floor has moved: the README, the package documentation and "+
			"the release notes all name the oldest supported daemon, and must move with it")
}

// TestNewDrawsTheLineAtTheFloor checks a daemon too old to be supported
// is refused when the Environment is created, rather than accepted and
// left to fail part-way through somebody's command — and that the oldest
// supported daemon is on the other side of that line.
func TestNewDrawsTheLineAtTheFloor(t *testing.T) {
	t.Parallel()

	connect := func(t *testing.T, version string) error {
		t.Helper()

		daemon := httptest.NewServer(daemonServing(version))
		t.Cleanup(daemon.Close)

		env, err := docker.New(t.Context(), "a-container", docker.WithHost("tcp://"+daemon.Listener.Addr().String()))
		if err == nil {
			t.Cleanup(func() { _ = env.Close() })
		}

		return err
	}

	t.Run("the version before the floor is refused", func(t *testing.T) {
		t.Parallel()

		require.Error(t, connect(t, "1.39"),
			"a daemon older than the documented floor must be refused when the environment is created")
	})

	t.Run("the floor itself is accepted", func(t *testing.T) {
		t.Parallel()

		require.NoError(t, connect(t, documentedFloor),
			"the oldest daemon the documentation names must be one the provider can reach")
	})
}

// daemonServing stands in for a daemon whose newest API version is the
// one given, as far as creating an Environment goes: it advertises that
// version, refuses a request for a newer one the way a daemon does, and
// reports any container asked about as running.
func daemonServing(version string) http.Handler {
	newest := minorVersion(version)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", version)

		// The ping carries no version of its own: it is how a client finds
		// out which to ask for.
		if strings.HasSuffix(r.URL.Path, "/_ping") {
			return
		}

		w.Header().Set("Content-Type", "application/json")

		requested, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v"), "/")

		switch {
		case minorVersion(requested) > newest:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"message":"client version `+requested+
				` is too new. Maximum supported API version is `+version+`"}`)
		case strings.HasPrefix(rest, "containers/") && strings.HasSuffix(rest, "/json"):
			_, _ = io.WriteString(w, `{"Id":"stand-in","State":{"Running":true}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"page not found"}`)
		}
	})
}

// minorVersion returns the minor part of an API version such as "1.40".
// Every Engine API version so far has had the major version 1.
func minorVersion(version string) int {
	_, minor, found := strings.Cut(version, ".")
	if !found {
		return 0
	}

	number, err := strconv.Atoi(minor)
	if err != nil {
		return 0
	}

	return number
}
