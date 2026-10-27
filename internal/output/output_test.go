package output_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/ruffel/invoke/internal/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errRefused = errors.New("refused")

// refusingWriter takes one write and refuses every one after it.
type refusingWriter struct {
	taken bytes.Buffer
	calls int
}

func (w *refusingWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls > 1 {
		return 0, errRefused
	}

	return w.taken.Write(p)
}

func TestSinkDrainsPastAFailingWriter(t *testing.T) {
	t.Parallel()

	w := &refusingWriter{}
	sink := output.NewSink(w)

	for _, chunk := range []string{"first ", "second ", "third"} {
		n, err := sink.Write([]byte(chunk))
		require.NoError(t, err, "a Sink never fails the copy delivering to it")
		assert.Len(t, chunk, n)
	}

	assert.Equal(t, "first ", w.taken.String(), "output stops reaching the writer at its first failure")
	assert.Equal(t, 2, w.calls, "a writer that failed is not asked again")
	require.ErrorIs(t, sink.Err(), errRefused, "the failure is kept for the outcome")
}

func TestNilWriterDiscards(t *testing.T) {
	t.Parallel()

	sink := output.NewSink(nil)

	n, err := sink.Write([]byte("gone"))
	require.NoError(t, err)
	assert.Equal(t, 4, n)
	require.NoError(t, sink.Err())
}

func TestFirstErrIsOrdered(t *testing.T) {
	t.Parallel()

	fine := output.NewSink(&bytes.Buffer{})
	_, _ = fine.Write([]byte("ok"))

	failed := output.NewSink(&refusingWriter{calls: 1})
	_, _ = failed.Write([]byte("refused"))

	require.NoError(t, output.FirstErr(fine, fine))
	require.ErrorIs(t, output.FirstErr(fine, failed), errRefused)
	require.ErrorIs(t, output.FirstErr(failed, fine), errRefused)
}
