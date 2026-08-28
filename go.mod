module github.com/ruffel/invoke

go 1.25.0

require (
	github.com/creack/pty v1.1.24
	github.com/pkg/sftp v1.13.11
	github.com/skeema/knownhosts v1.3.2
	github.com/stretchr/testify v1.12.1
	golang.org/x/crypto v0.55.0
)

require (
	github.com/kr/fs v0.1.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

// Pre-rewrite layout whose providers/* submodule tags were deleted;
// these versions resolve but cannot build.
retract [v0.0.1, v0.1.0]
