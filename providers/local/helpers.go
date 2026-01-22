package local

import (
	"context"

	"github.com/ruffel/invoke"
)

// RunCommand executes a fully configured command locally using a new environment.
func RunCommand(ctx context.Context, cmd *invoke.Command, opts ...invoke.ExecOption) (*invoke.BufferedResult, error) {
	env, err := New()
	if err != nil {
		return nil, err
	}

	defer func() { _ = env.Close() }()

	exec := invoke.NewExecutor(env)

	return exec.RunBuffered(ctx, cmd, opts...)
}

// RunShell executes a shell command string locally using a new environment.
func RunShell(ctx context.Context, script string, opts ...invoke.ExecOption) (*invoke.BufferedResult, error) {
	env, err := New()
	if err != nil {
		return nil, err
	}

	defer func() { _ = env.Close() }()

	exec := invoke.NewExecutor(env)

	return exec.RunShell(ctx, script, opts...)
}
