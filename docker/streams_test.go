//go:build docker

package docker_test

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStderrAloneReachesStderr checks output written only to standard
// error arrives there, and nothing arrives on standard output.
//
// Without a terminal the daemon sends both streams down one connection,
// each chunk labelled with the stream it belongs to. A command that
// writes to one stream alone is where a mislabelled chunk has nowhere to
// hide: there is no output on the other stream to be mistaken for it.
func TestStderrAloneReachesStderr(t *testing.T) {
	t.Parallel()

	env := dialContainer(t)

	var stdout, stderr bytes.Buffer

	_, err := invoke.NewExecutor(env).Run(t.Context(), invoke.Shell("echo diagnostic 1>&2"),
		invoke.IO{Stdout: &stdout, Stderr: &stderr})
	require.NoError(t, err, "the command failed")

	assert.Empty(t, stdout.String(),
		"standard output must stay empty when the command wrote only to standard error")
	assert.Equal(t, "diagnostic\n", stderr.String(),
		"standard error must carry exactly what the command wrote to it")
}

// TestInterleavedStreamsStaySeparate checks two streams written in
// alternation, many times over, each arrive whole and in order.
//
// The contract suite checks the streams stay apart for a line each, and
// that a large output arrives complete when one stream is written after
// the other. Neither exercises what sharing a connection makes hard:
// thousands of small chunks for alternating streams, with a read ending
// wherever it happens to. Every line is numbered, so a chunk delivered to
// the wrong stream, dropped, or reordered changes the content rather than
// only the length.
func TestInterleavedStreamsStaySeparate(t *testing.T) {
	t.Parallel()

	const lines = 20000

	env := dialContainer(t)

	script := `i=0; while [ "$i" -lt ` + strconv.Itoa(lines) + ` ]; do ` +
		`echo "out $i"; echo "err $i" 1>&2; i=$((i+1)); done`

	var stdout, stderr bytes.Buffer

	_, err := invoke.NewExecutor(env).Run(t.Context(), invoke.Shell(script),
		invoke.IO{Stdout: &stdout, Stderr: &stderr})
	require.NoError(t, err, "the command failed")

	assert.Empty(t, firstUnexpectedLine("out", lines, stdout.String()),
		"standard output must carry every line written to it, in order, and nothing else")
	assert.Empty(t, firstUnexpectedLine("err", lines, stderr.String()),
		"standard error must carry every line written to it, in order, and nothing else")
}

// firstUnexpectedLine describes the first place got departs from count
// lines of the form "prefix N", or returns the empty string when it does
// not. Naming the line keeps a failure readable: the two outputs differ
// by one chunk in several hundred kilobytes.
func firstUnexpectedLine(prefix string, count int, got string) string {
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")

	for i := range count {
		want := prefix + " " + strconv.Itoa(i)

		if i >= len(lines) {
			return "output ended before " + strconv.Quote(want)
		}

		if lines[i] != want {
			return "line " + strconv.Itoa(i) + " is " + strconv.Quote(lines[i]) + ", not " + strconv.Quote(want)
		}
	}

	if len(lines) > count {
		return "output continues past the last line with " + strconv.Quote(lines[count])
	}

	return ""
}
