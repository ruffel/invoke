module github.com/ruffel/invoke/providers/ssh

go 1.24.0

require (
	github.com/kevinburke/ssh_config v1.6.0
	github.com/pkg/sftp v1.13.10
	github.com/ruffel/invoke v0.0.0
	github.com/stretchr/testify v1.11.1
	golang.org/x/crypto v0.48.0
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/google/shlex v0.0.0-20191202100458-e7afc7fbc510 // indirect
	github.com/kr/fs v0.1.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	golang.org/x/term v0.40.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace (
	github.com/ruffel/invoke => ../../
	github.com/ruffel/invoke/providers/docker => ../docker
	github.com/ruffel/invoke/providers/local => ../local
	github.com/ruffel/invoke/providers/mock => ../mock
)
