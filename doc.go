// Package invoke runs commands and transfers files on execution targets —
// the local machine, remote hosts over SSH, and containers — through one
// provider-agnostic interface.
//
// The Docker provider lives in a separate module,
// github.com/ruffel/invoke/docker, so its dependency tree stays out of the
// graph of anyone who does not use it.
//
// # Getting started
//
// An [Environment] is a target. An [Executor] wraps one and adds the
// policy most callers want: capturing output, retrying, running under
// sudo. Constructing the target is the only line that changes between
// them.
//
//	env, err := local.New()          // or ssh.New(ctx, host, ...), docker.New(ctx, container, ...)
//	if err != nil {
//	    return err
//	}
//	defer env.Close()
//
//	_, stdout, _, err := invoke.NewExecutor(env).Output(ctx, invoke.New("uname", "-s"))
//
// [Command] is a plain value describing what to run. It carries no
// streams and no state, so one can be run repeatedly, or against several
// targets, without being rebuilt. Where the output goes is a property of
// the invocation rather than the command, and lives in [IO].
//
// # Knowing what happened
//
// Every failure answers one question first: did the command run?
//
//   - [ExitError] — it ran and failed. The exit status is trustworthy.
//   - [TransportError] — the connection to the target failed. Nothing can
//     be concluded about the command.
//   - [ErrNotFound], [ErrInvalidWorkdir] — it could not be started.
//   - [ErrClosed] — the caller stopped it, or the environment was closed.
//   - [ErrNotSupported] — the target cannot do what was asked.
//
// Anything a provider cannot classify stays an ordinary error, and
// ordinary errors are never retried. Retrying is something a provider has
// to earn by naming a failure as transient.
//
// # Retries
//
// [WithRetry] re-runs only [TransportError], because it is the only
// family that says the command did not run.
//
// That distinction is sharper than it first looks. A connection dying
// under a running command is a transport failure by any ordinary reading,
// but the command may already have taken effect and nothing on this side
// can tell whether it did. Retrying would run an arbitrary command a
// second time, so such a failure is terminal and the error says so.
//
// File transfers are the exception, and only because they are built to
// be: each is delivered whole or not at all, so repeating one is safe.
//
// # Optional features
//
// Targets differ in what they can do, and say so through [Capabilities].
// The rule is symmetric, and the contract suite enforces both halves: a
// declared capability must work, and an undeclared one must fail with
// [ErrNotSupported] rather than being quietly ignored.
//
//   - TTY — allocated by every target that runs real processes.
//   - Signals — delivered by every target, though a container must have a
//     shell for one to reach the right process; without a shell the
//     capability is not declared. The docker package documents why, and
//     the ssh package documents the one thing its protocol will not
//     confirm. A signal always reaches the process it names; whether it
//     also reaches that process's children is a property of the target,
//     described on [Process.Signal].
//   - SymlinkPreserve — honored by every target.
//
// # Testing
//
// The fake package is a working target that keeps its filesystem in
// memory. It is not a mock: it passes the same contract suite the real
// providers do, so a test written against it cannot come to rely on
// behavior no real target has.
//
// The invoketest package is that suite. Any Environment implementation,
// in this module or outside it, can be run against it.
package invoke
