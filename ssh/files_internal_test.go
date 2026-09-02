package ssh

import (
	"testing"

	"github.com/ruffel/invoke/internal/transfer"
	"github.com/stretchr/testify/assert"
)

// TestSFTPSideAdvertisesConcurrency pins the default that lets tree
// transfers overlap their per-file round trips without the caller
// asking. Losing it would break nothing visible — every transfer would
// still deliver — it would just quietly cost the tree speedup, which is
// exactly the kind of regression a test has to name.
func TestSFTPSideAdvertisesConcurrency(t *testing.T) {
	t.Parallel()

	var hinter transfer.ConcurrencyHinter = sftpFS{}

	assert.Greater(t, hinter.CopyConcurrency(), 1,
		"the SFTP side must prefer copying several files at once; its per-file cost is round trips")
}
