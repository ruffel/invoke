package docker

import (
	"bufio"
	"context"
	"math"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// settleStubClient serves a scripted exec whose inspect keeps calling
// the command running for a few polls after its stream has ended — the
// window a real daemon is entitled to. While an exec runs, its inspect
// carries exit code zero.
type settleStubClient struct {
	client.APIClient

	runningPolls int // inspects answered Running before settling
	exitCode     int // the settled status

	mu       sync.Mutex
	inspects int
}

func (c *settleStubClient) ContainerExecCreate(_ context.Context, _ string, _ container.ExecOptions) (container.ExecCreateResponse, error) {
	return container.ExecCreateResponse{ID: "exec-under-test"}, nil
}

func (c *settleStubClient) ContainerExecAttach(_ context.Context, _ string, _ container.ExecAttachOptions) (types.HijackedResponse, error) {
	server, clientSide := net.Pipe()

	// The command produces no output: its stream ends immediately,
	// which is exactly when the daemon may still call it running.
	_ = server.Close()

	return types.HijackedResponse{Conn: clientSide, Reader: bufio.NewReader(clientSide)}, nil
}

func (c *settleStubClient) ContainerExecInspect(_ context.Context, _ string) (container.ExecInspect, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.inspects++
	if c.inspects <= c.runningPolls {
		return container.ExecInspect{Running: true}, nil
	}

	return container.ExecInspect{Running: false, ExitCode: c.exitCode}, nil
}

// TestRunRawWaitsForTheExecToStop pins runRaw to the same settling
// discipline the main wait path uses: an inspect answered while the
// exec still runs carries exit code zero, and trusting it mistakes an
// unfinished command for a successful one — a racing LookPath returns
// an empty path with a nil error, a pre-check passes a workdir it never
// verified, a kill reports success for a kill that failed.
func TestRunRawWaitsForTheExecToStop(t *testing.T) {
	t.Parallel()

	const settledExit = 7

	stub := &settleStubClient{runningPolls: 3, exitCode: settledExit}
	env := &Environment{client: stub, cfg: &Config{Timeout: 2 * time.Second}}

	_, code, err := env.runRaw(t.Context(), []string{"probe"})
	require.NoError(t, err)

	assert.Equal(t, settledExit, code,
		"runRaw must report the exec's settled status, not the zero an unfinished inspect carries")
}

// TestRunRawRefusesAnExecThatNeverStops pins the bounded half: a status
// that never settles is reported as unresolved, never invented.
func TestRunRawRefusesAnExecThatNeverStops(t *testing.T) {
	t.Parallel()

	stub := &settleStubClient{runningPolls: math.MaxInt}
	env := &Environment{client: stub, cfg: &Config{Timeout: 500 * time.Millisecond}}

	_, code, err := env.runRaw(t.Context(), []string{"probe"})

	var transportErr *invoke.TransportError

	require.ErrorAs(t, err, &transportErr,
		"a status that never settles must be reported as unresolved, not invented as exit zero")
	assert.Equal(t, -1, code)
}
