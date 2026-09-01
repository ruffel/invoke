//go:build openssh

// Transfer benchmarks against a real OpenSSH server, sharing the
// container harness and build tag of the contract tests in this lane.
//
// They exist so transfer performance work carries before/after numbers
// measured through Upload and Download proper rather than through a
// standalone SFTP client. Two shapes, because transfer cost has two
// independent axes: one large file is bound by bytes per round trip,
// and a tree of packet-sized files is bound by round trips per file.
//
// Run with:
//
//	go test -tags openssh -run '^$' -bench . ./ssh
//
// A single iteration takes seconds, so the default -benchtime stops
// after one; -benchtime=3x averages over more.

package ssh_test

import (
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	// largeFileSize is one file big enough that per-file overhead
	// vanishes and steady-state throughput is what is measured.
	largeFileSize = 256 << 20

	// treeFileCount and treeFileSize describe a tree whose files fit in
	// a single SFTP packet each, so what is measured is the per-file
	// request chain rather than data movement.
	treeFileCount = 2000
	treeFileSize  = 4 << 10
)

func BenchmarkUpload(b *testing.B) {
	env := dialOpenSSH(b, startOpenSSH(b))

	b.Run("large_file", func(b *testing.B) {
		local := largeLocalFile(b)

		b.SetBytes(largeFileSize)

		for b.Loop() {
			require.NoError(b, env.Upload(b.Context(), local, "bench-large"))
		}
	})

	b.Run("small_tree", func(b *testing.B) {
		local := localTree(b)

		b.SetBytes(treeFileCount * treeFileSize)

		for b.Loop() {
			require.NoError(b, env.Upload(b.Context(), local, "bench-tree"))
		}

		reportFilesPerSecond(b)
	})
}

func BenchmarkDownload(b *testing.B) {
	env := dialOpenSSH(b, startOpenSSH(b))

	b.Run("large_file", func(b *testing.B) {
		require.NoError(b, env.Upload(b.Context(), largeLocalFile(b), "bench-large"))

		local := filepath.Join(b.TempDir(), "large")

		b.SetBytes(largeFileSize)

		for b.Loop() {
			require.NoError(b, env.Download(b.Context(), "bench-large", local))
		}
	})

	b.Run("small_tree", func(b *testing.B) {
		require.NoError(b, env.Upload(b.Context(), localTree(b), "bench-tree"))

		local := filepath.Join(b.TempDir(), "tree")

		b.SetBytes(treeFileCount * treeFileSize)

		for b.Loop() {
			require.NoError(b, env.Download(b.Context(), "bench-tree", local))
		}

		reportFilesPerSecond(b)
	})
}

// reportFilesPerSecond restates the tree benchmark's rate per file,
// which is the number tree-shaped transfer work aims at.
func reportFilesPerSecond(b *testing.B) {
	b.Helper()

	b.ReportMetric(float64(treeFileCount)*float64(b.N)/b.Elapsed().Seconds(), "files/s")
}

// largeLocalFile writes largeFileSize of random bytes under the
// benchmark's temp directory. Random, so nothing underneath can
// deduplicate or compress its way past the measurement.
func largeLocalFile(b *testing.B) string {
	b.Helper()

	path := filepath.Join(b.TempDir(), "large")

	f, err := os.Create(path)
	require.NoError(b, err, "creating the local payload")

	_, err = io.CopyN(f, rand.Reader, largeFileSize)
	require.NoError(b, err, "filling the local payload")
	require.NoError(b, f.Close())

	return path
}

// localTree writes treeFileCount files of treeFileSize each into one
// directory under the benchmark's temp directory.
func localTree(b *testing.B) string {
	b.Helper()

	dir := filepath.Join(b.TempDir(), "tree")
	require.NoError(b, os.Mkdir(dir, 0o755))

	content := make([]byte, treeFileSize)
	_, err := rand.Read(content)
	require.NoError(b, err)

	for i := range treeFileCount {
		name := filepath.Join(dir, fmt.Sprintf("f%04d", i))
		require.NoError(b, os.WriteFile(name, content, 0o644))
	}

	return dir
}
