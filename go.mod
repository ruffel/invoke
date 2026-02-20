module github.com/ruffel/invoke

go 1.24.0

require (
	github.com/google/shlex v0.0.0-20191202100458-e7afc7fbc510
	github.com/ruffel/invoke/providers/local v0.0.0-00010101000000-000000000000
	github.com/ruffel/invoke/providers/mock v0.0.0-00010101000000-000000000000
	github.com/ruffel/invoke/providers/ssh v0.0.0-00010101000000-000000000000
	github.com/stretchr/testify v1.11.1
	golang.org/x/term v0.40.0
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/kevinburke/ssh_config v1.4.0 // indirect
	github.com/kr/fs v0.1.0 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/pkg/sftp v1.13.10 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	github.com/stretchr/objx v0.5.3 // indirect
	golang.org/x/crypto v0.47.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace (
	github.com/ruffel/invoke/providers/docker => ./providers/docker
	github.com/ruffel/invoke/providers/local => ./providers/local
	github.com/ruffel/invoke/providers/mock => ./providers/mock
	github.com/ruffel/invoke/providers/ssh => ./providers/ssh
)
