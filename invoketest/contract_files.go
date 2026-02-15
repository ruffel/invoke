package invoketest

import (
	"path/filepath"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/require"
)

func fileContracts() []TestCase {
	return []TestCase{
		{
			Category:    CategoryFilesystem,
			Name:        "upload-source-missing",
			Description: "Error returned when we try to upload a non-existent local file",
			Run: func(t T, env invoke.Environment) {
				// Create a directory that we know exists, but look for a file inside it that does NOT exist.
				src := filepath.Join(t.TempDir(), "this-file-really-does-not-exist-12345")

				dst := "/tmp/should-not-exist-on-target-12345"
				if env.TargetOS() == invoke.OSWindows {
					dst = `C:\should-not-exist-on-target-12345`
				}

				err := env.Upload(t.Context(), src, dst)
				require.Error(t, err)
			},
		},
	}
}
