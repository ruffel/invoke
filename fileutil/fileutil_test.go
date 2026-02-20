package fileutil

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
