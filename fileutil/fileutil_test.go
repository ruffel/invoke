package fileutil

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProgressReader_UpdatesCurrentAndCallsFn(t *testing.T) {
	t.Parallel()

	data := "hello world"

	var calls []struct{ current, total int64 }

	pr := &ProgressReader{
		Reader: strings.NewReader(data),
		Total:  int64(len(data)),
		Fn: func(current, total int64) {
			calls = append(calls, struct{ current, total int64 }{current, total})
		},
	}

	buf, err := io.ReadAll(pr)
	require.NoError(t, err)
	assert.Equal(t, data, string(buf))
	assert.Equal(t, int64(len(data)), pr.Current)
	require.NotEmpty(t, calls)

	last := calls[len(calls)-1]
	assert.Equal(t, int64(len(data)), last.current)
	assert.Equal(t, int64(len(data)), last.total)
}

func TestProgressReader_NilFnDoesNotPanic(t *testing.T) {
	t.Parallel()

	pr := &ProgressReader{
		Reader: strings.NewReader("data"),
		Total:  4,
		Fn:     nil,
	}

	buf, err := io.ReadAll(pr)
	require.NoError(t, err)
	assert.Equal(t, "data", string(buf))
	assert.Equal(t, int64(4), pr.Current)
}

func TestProgressReader_PropagatesEOF(t *testing.T) {
	t.Parallel()

	pr := &ProgressReader{
		Reader: strings.NewReader(""),
		Total:  0,
	}

	buf := make([]byte, 10)
	n, err := pr.Read(buf)
	assert.Equal(t, 0, n)
	assert.ErrorIs(t, err, io.EOF)
}

func TestContextReader_CancelledContextReturnsError(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cr := &ContextReader{
		Ctx:    ctx,
		Reader: strings.NewReader("should not read"),
	}

	buf := make([]byte, 10)
	n, err := cr.Read(buf)
	assert.Equal(t, 0, n)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestContextReader_ActiveContextDelegates(t *testing.T) {
	t.Parallel()

	cr := &ContextReader{
		Ctx:    context.Background(),
		Reader: strings.NewReader("hello"),
	}

	buf, err := io.ReadAll(cr)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(buf))
}

func TestCheckPathTraversal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		root      string
		target    string
		expectErr bool
	}{
		{
			name:      "Safe child",
			root:      "/tmp/safe",
			target:    "/tmp/safe/child.txt",
			expectErr: false,
		},
		{
			name:      "Safe deep child",
			root:      "/tmp/safe",
			target:    "/tmp/safe/dir/child.txt",
			expectErr: false,
		},
		{
			name:      "Root itself",
			root:      "/tmp/safe",
			target:    "/tmp/safe",
			expectErr: false,
		},
		{
			name:      "Traversal attempt",
			root:      "/tmp/safe",
			target:    "/tmp/safe/../evil.txt",
			expectErr: true,
		},
		{
			name:      "Direct parent traversal",
			root:      "/tmp/safe",
			target:    "/tmp/evil.txt",
			expectErr: true,
		},
		{
			name:      "Root prefix but not child",
			root:      "/tmp/safe",
			target:    "/tmp/safe_suffix_is_not_child",
			expectErr: true,
		},
		{
			name:      "Relative paths safe",
			root:      "safe",
			target:    "safe/child",
			expectErr: false,
		},
		{
			name:      "Relative paths unsafe",
			root:      "safe",
			target:    "safe/../evil",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Normalize for OS (Windows vs Unix)
			root := filepath.FromSlash(tt.root)
			target := filepath.FromSlash(tt.target)

			err := CheckPathTraversal(root, target)
			if tt.expectErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "illegal file path")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCheckRemotePathTraversal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		root      string
		target    string
		expectErr bool
	}{
		{
			name:      "Safe child",
			root:      "/home/user/data",
			target:    "/home/user/data/file.txt",
			expectErr: false,
		},
		{
			name:      "Root itself",
			root:      "/home/user/data",
			target:    "/home/user/data",
			expectErr: false,
		},
		{
			name:      "Traversal attempt",
			root:      "/home/user/data",
			target:    "/home/user/data/../evil.txt",
			expectErr: true,
		},
		{
			name:      "Root prefix but not child",
			root:      "/home/user/data",
			target:    "/home/user/data_suffix",
			expectErr: true,
		},
		{
			name:      "Trailing slash root",
			root:      "/home/user/data/",
			target:    "/home/user/data/file.txt",
			expectErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := CheckRemotePathTraversal(tt.root, tt.target)
			if tt.expectErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "illegal remote file path")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
