//go:build unix

package local

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestClampDimPinsToTheMaximum pins the ceiling: a dimension beyond the
// wire format's sixteen bits pins to the maximum instead of wrapping to
// something unrelated — 70000 must not become 4464.
func TestClampDimPinsToTheMaximum(t *testing.T) {
	t.Parallel()

	assert.Equal(t, uint16(80), clampDim(80), "an ordinary dimension passes through")
	assert.Equal(t, uint16(math.MaxUint16), clampDim(math.MaxUint16), "the maximum itself passes through")
	assert.Equal(t, uint16(math.MaxUint16), clampDim(70000), "beyond the maximum pins, never wraps")
}
