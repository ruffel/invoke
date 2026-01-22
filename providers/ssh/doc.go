// Package ssh provides an implementation of the invoke.Environment interface
// for remote servers via the SSH protocol.
//
// It utilizes "golang.org/x/crypto/ssh" to manage sessions, providing robust
// support for:
//   - Interactive sessions with PTY allocation
//   - Signal propagation (Interrupt, Kill)
//   - Standard blocking executions
//   - File transfers (Upload/Download) via SFTP
//
// The provider handles the complexities of session lifecycle management,
// stream bridging, and connection maintenance.
//
// Usage:
//
//	config := ssh.NewConfig("example.com", "user")
//	config.Password = "secret"
//	env, err := ssh.New(config)
package ssh
