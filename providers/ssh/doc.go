// Package ssh provides an implementation of the invoke.Environment interface
// for remote command execution over SSH.
//
// It wraps "golang.org/x/crypto/ssh" and supports:
//   - Interactive sessions with PTY allocation
//   - Signal propagation (Interrupt, Kill)
//   - File transfers (Upload/Download) via SFTP
//
// Usage:
//
//	env, err := ssh.New(
//	    ssh.WithHost("example.com"),
//	    ssh.WithUser("deploy"),
//	    ssh.WithKeyPath("~/.ssh/id_ed25519"),
//	    ssh.WithInsecureSkipVerify(true), // testing only
//	)
package ssh
