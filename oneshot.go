package invoke

import "context"

// Run starts cmd on env with the given IO, waits for it, and returns the
// outcome. It is the one-shot form of [Executor.Run]; construct an
// Executor to state retry or sudo defaults once and share them across
// calls.
func Run(ctx context.Context, env Environment, cmd Command, stdio IO, opts ...Option) (Result, error) {
	return NewExecutor(env).Run(ctx, cmd, stdio, opts...)
}

// Text runs cmd on env and returns its standard output as a string with
// trailing carriage returns and newlines removed. It is the one-shot
// form of [Executor.Text].
func Text(ctx context.Context, env Environment, cmd Command, opts ...Option) (string, error) {
	return NewExecutor(env).Text(ctx, cmd, opts...)
}
