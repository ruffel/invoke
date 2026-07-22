package ssh_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSeveredConnectionDuringCommandIsTerminal checks a link that dies
// under a running command is NOT reported as a retryable transport
// failure.
//
// The command may already have taken effect, and nothing on this side can
// tell whether it did. Classifying it retryable would let the executor
// run an arbitrary command a second time — the asymmetry with transfers,
// which are retryable precisely because their delivery is atomic.
//
// One outage surfaces several ways depending on which part of the session
// notices it first: a missing exit status, a broken channel, a read error
// on the connection. The caller did not choose which, so all of them must
// classify alike or retryability becomes a coin flip on identical
// outages. This repeats to cover whichever the timing produces.
func TestSeveredConnectionDuringCommandIsTerminal(t *testing.T) {
	t.Parallel()

	const attempts = 8

	for i := range attempts {
		srv := startTestServer(t)
		env := dialServer(t, srv)

		proc, err := env.Start(t.Context(), invoke.New("sleep", "30"), invoke.IO{})
		require.NoErrorf(t, err, "attempt %d", i)

		// Let the command settle, then drop the connection under it.
		time.Sleep(50 * time.Millisecond)
		srv.sever()

		_, waitErr := proc.Wait()
		require.Errorf(t, waitErr, "attempt %d: Wait after the connection died reported success", i)

		var transportErr *invoke.TransportError

		assert.NotErrorAsf(t, waitErr, &transportErr,
			"attempt %d: Wait after the connection died must be a terminal error: "+
				"an interrupted command must not be retried automatically", i)

		// The reason must be legible: "no exit status" alone does not tell
		// a caller their command may have half-run.
		assert.ErrorContainsf(t, waitErr, "may or may not",
			"attempt %d: the error does not report the outcome as unknown", i)
	}
}

// TestSeveredConnectionDuringTransferIsTransportError checks the same for
// a transfer.
//
// One outage surfaces two ways depending on which side of the SFTP client
// observes it first: the receive loop broadcasting a connection-lost
// status, or a packet send reporting a raw write failure. Both must
// classify the same way, or retryability becomes a coin flip on identical
// outages — so this repeats to cover both.
func TestSeveredConnectionDuringTransferIsTransportError(t *testing.T) {
	t.Parallel()

	const (
		attempts = 8
		fileSize = 8 << 20
	)

	src := filepath.Join(t.TempDir(), "big.bin")
	require.NoError(t, os.WriteFile(src, []byte(strings.Repeat("x", fileSize)), 0o600), "writing fixture")

	for i := range attempts {
		srv := startTestServer(t)
		env := dialServer(t, srv)

		dst := filepath.Join(t.TempDir(), "out.bin")

		// Sever once the transfer is provably under way, so the failure
		// lands mid-stream rather than at setup.
		severed := make(chan struct{})

		var once bool

		err := env.Upload(t.Context(), src, dst, invoke.WithProgress(func(p invoke.TransferProgress) {
			if !once && p.Current > 0 {
				once = true

				srv.sever()
				close(severed)
			}
		}))

		select {
		case <-severed:
		default:
			require.Failf(t, "the transfer finished before it could be severed", "attempt %d", i)
		}

		require.Errorf(t, err, "attempt %d: Upload over a dead connection reported success", i)

		var transportErr *invoke.TransportError

		assert.ErrorAsf(t, err, &transportErr, "attempt %d: Upload after the connection died, want a TransportError", i)
	}
}
