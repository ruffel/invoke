package ssh_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ruffel/invoke"
)

// TestSeveredConnectionDuringCommandIsTerminal checks a link that dies
// under a running command is NOT reported as a retryable transport
// failure.
//
// The command may already have taken effect, and nothing on this side can
// tell whether it did. Classifying it retryable would let the executor
// run an arbitrary command a second time — the asymmetry with transfers,
// which are retryable precisely because their delivery is atomic.
func TestSeveredConnectionDuringCommandIsTerminal(t *testing.T) {
	t.Parallel()

	srv := startTestServer(t)
	env := dialServer(t, srv)

	proc, err := env.Start(t.Context(), invoke.New("sleep", "30"), invoke.IO{})
	if err != nil {
		t.Fatalf("Start = %v", err)
	}

	// Let the command settle, then drop the connection under it.
	time.Sleep(50 * time.Millisecond)
	srv.sever()

	_, waitErr := proc.Wait()
	if waitErr == nil {
		t.Fatal("Wait after the connection died reported success")
	}

	var transportErr *invoke.TransportError
	if errors.As(waitErr, &transportErr) {
		t.Errorf("Wait after the connection died = %v, want a terminal error: "+
			"an interrupted command must not be retried automatically", waitErr)
	}

	// The reason must be legible: "no exit status" alone does not tell a
	// caller their command may have half-run.
	if !strings.Contains(waitErr.Error(), "may or may not") {
		t.Errorf("error %q does not report the outcome as unknown", waitErr)
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
	if err := os.WriteFile(src, []byte(strings.Repeat("x", fileSize)), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

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
			t.Fatalf("attempt %d: the transfer finished before it could be severed", i)
		}

		if err == nil {
			t.Fatalf("attempt %d: Upload over a dead connection reported success", i)
		}

		var transportErr *invoke.TransportError
		if !errors.As(err, &transportErr) {
			t.Errorf("attempt %d: Upload after the connection died = %v, want a TransportError", i, err)
		}
	}
}
