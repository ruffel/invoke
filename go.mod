module github.com/ruffel/invoke

go 1.25.0

require (
	github.com/creack/pty v1.1.24
	github.com/pkg/sftp v1.13.11
	github.com/skeema/knownhosts v1.3.2
	github.com/stretchr/testify v1.11.1
	golang.org/x/crypto v0.55.0
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/kr/fs v0.1.0 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	golang.org/x/sys v0.47.0 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// Pre-rewrite layout whose providers/* submodule tags were deleted;
// these versions resolve but cannot build.
retract [v0.0.1, v0.1.0]
