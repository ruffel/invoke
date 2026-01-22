package ssh

import (
	"path"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDownloadDir_PathLogic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		remoteBase string
		remotePath string
		wantRel    string
		wantErr    bool
	}{
		{
			name:       "basic nested file",
			remoteBase: "/home/user/data",
			remotePath: "/home/user/data/subdir/file.txt",
			wantRel:    "subdir/file.txt",
		},
		{
			name:       "trailing slash on base",
			remoteBase: "/home/user/data/",
			remotePath: "/home/user/data/subdir/file.txt",
			wantRel:    "subdir/file.txt",
		},
		{
			name:       "partial component match (should fail)",
			remoteBase: "/home/user/data",
			remotePath: "/home/user/datapath/file.txt",
			wantErr:    true,
		},
		{
			name:       "root as base",
			remoteBase: "/",
			remotePath: "/etc/passwd",
			wantRel:    "etc/passwd",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cleanBase := path.Clean(tt.remoteBase)
			if cleanBase == "/" {
				cleanBase = ""
			}

			isWithin := strings.HasPrefix(tt.remotePath, cleanBase+"/")
			if tt.wantErr {
				assert.Falsef(t, isWithin, "expected %q NOT to be within %q", tt.remotePath, tt.remoteBase)

				return
			}

			if assert.Truef(t, isWithin, "expected %q to be within %q", tt.remotePath, tt.remoteBase) {
				relPath := strings.TrimPrefix(tt.remotePath, cleanBase+"/")
				assert.Equal(t, tt.wantRel, relPath)
			}
		})
	}
}

func TestDownloadDir_RootEntrySkipUsesCleanPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		remoteBase string
		walkPath   string
		wantSkip   bool
	}{
		{
			name:       "trailing slash base should skip cleaned root entry",
			remoteBase: "/home/user/data/",
			walkPath:   "/home/user/data",
			wantSkip:   true,
		},
		{
			name:       "canonical base should skip same entry",
			remoteBase: "/home/user/data",
			walkPath:   "/home/user/data",
			wantSkip:   true,
		},
		{
			name:       "child path should not skip",
			remoteBase: "/home/user/data/",
			walkPath:   "/home/user/data/child.txt",
			wantSkip:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cleanBase := path.Clean(tt.remoteBase)
			if cleanBase == "/" {
				cleanBase = ""
			}

			cleanPath := path.Clean(tt.walkPath)
			assert.Equal(t, tt.wantSkip, cleanPath == cleanBase)
		})
	}
}
