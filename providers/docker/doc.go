// Package docker provides an implementation of the invoke.Environment interface
// for executing commands inside running Docker containers.
//
// It interacts with the Docker Engine API to create exec instances and supports:
//   - Automatic stream demultiplexing for non-TTY commands
//   - Context-aware cancellation
//   - Local socket and remote TCP Docker hosts
//   - File transfers via tar archives
//
// Usage:
//
//	env, err := docker.New(docker.WithContainerID("my-container"))
package docker
