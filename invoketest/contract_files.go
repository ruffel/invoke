package invoketest

import (
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
				err := env.Upload(t.Context(), "/non-existent-file-path-12345", "/tmp/should-not-exist")
				require.Error(t, err)
			},
		},
	}
}
