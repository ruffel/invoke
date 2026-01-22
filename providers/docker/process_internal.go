package docker

import (
	"github.com/docker/docker/api/types/container"
	"github.com/ruffel/invoke"
)

// buildExecConfig translates invoke.Command to container.ExecOptions.
func buildExecConfig(cmd *invoke.Command) container.ExecOptions {
	return container.ExecOptions{
		Cmd:          append([]string{cmd.Cmd}, cmd.Args...),
		Env:          cmd.Env,
		WorkingDir:   cmd.Dir,
		AttachStdout: true,
		AttachStderr: true,
		AttachStdin:  cmd.Stdin != nil,
		Tty:          cmd.Tty,
	}
}

// buildAttachConfig creates the configuration for attaching to a Docker exec instance.
func buildAttachConfig(cmd *invoke.Command) container.ExecStartOptions {
	return container.ExecStartOptions{
		Tty: cmd.Tty,
	}
}
