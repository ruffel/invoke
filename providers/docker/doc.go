// Package docker provides an implementation of the invoke.Environment interface
// for executing commands inside existing Docker containers.
//
// It interacts directly with the Docker Engine API to create 'exec' instances.
// Key features include:
//   - Automatic stream demultiplexing for non-TTY commands (stdout vs stderr)
//   - Context-aware cancellation using process signaling
//   - Support for both local socket and remote TCP Docker hosts
//   - File transfer support (via "docker cp" emulation)
//
// This provider allows you to treat a container as just another execution
// environment, abstracting away the API details.
//
// Usage:
//
//	// Connects to default socket
//	env, err := docker.New("my-container-id")
package docker
