# Lifecycle Contract

This document describes the lifecycle behavior `invoke` is trying to guarantee.

It is meant to be practical, not exhaustive. The point is to make the behavior
around `Run`, `Start`, `Wait`, `Signal`, `Close`, and context cancellation clear
enough that we can implement it consistently and test it across the first-party
providers.

## Scope

This contract applies to the first-party providers:

- local
- SSH
- Docker

Providers may still have transport-specific caveats, but those caveats should sit
on top of the rules below rather than contradict them.

## Environments

An `Environment` is a reusable execution context. For local that means process
execution on the current host. For SSH and Docker it also means holding onto
provider-level resources such as a client connection.

An environment is not meant to be one-shot. A caller should be able to reuse the
same environment for multiple commands until it is closed.

The expected behavior is:

- `Close` is idempotent.
- After `Close`, new `Run`, `Start`, `LookPath`, `Upload`, and `Download` calls fail deterministically.
- For first-party providers, post-close environment operations should wrap `invoke.ErrEnvironmentClosed`.
- A single environment should support multiple sequential commands.
- A single environment should also allow more than one started process to exist at the same time.

What the contract does not promise is scheduling or isolation between concurrent
operations. If a caller needs strict serialization, they should do that outside
the library.

## Processes

A `Process` represents one started command.

### Start

`Start` validates the command and either returns a usable process handle or
returns an error. If `Start` fails, callers should assume there is no live
process to manage.

### Run

`Run` is the synchronous path. At the contract level, it should behave like
`Start` followed by `Wait`, including how exit failures are reported.

### Wait

`Wait` blocks until the command reaches a terminal state.

The expected behavior is:

- successful completion returns `(*Result, nil)`
- non-zero exit returns `(*Result, *invoke.ExitError)`
- calling `Wait` again after the process has finished remains safe and returns the same logical outcome

### Close

`Close` is the terminal cleanup operation for a process handle.

The expected behavior is:

- `Close` is idempotent
- if the process is still running, `Close` makes a best effort to stop it and release resources
- `Close` does not block indefinitely
- after `Close`, callers should treat the process handle as closed and should not rely on `Wait` or `Signal` succeeding

## Signals

Signal behavior is provider-dependent, so the contract here is intentionally
modest.

The expected behavior is:

- signaling a closed process returns an error
- supported signals follow provider-specific behavior
- unsupported signal or transport combinations return an error
- when the feature itself is unsupported by a first-party provider, the error should converge on `invoke.ErrNotSupported`

## Context Cancellation

All blocking operations are context-aware.

The expected behavior is:

- if a process is started with a cancellable context and that context is canceled, `Wait` returns in bounded time
- cancellation may surface as a provider or transport error rather than an `ExitError`
- cancellation should not leave a first-party provider process hanging indefinitely
- one canceled process should not leave unrelated waits blocked forever

## TTY and Interactive Sessions

TTY support matters, but it is not part of the strongest parity promise for this hardening phase.

Interactive sessions are harder to normalize than ordinary command execution,
especially for SSH. Terminal allocation, raw mode, signal forwarding, window
resizing, and stream behavior all vary by transport.

For now, the contract is deliberately narrow:

- non-interactive command execution is the primary behavior we are hardening first
- TTY support may be provider-specific
- if a provider does not support TTY for a given operation, it should fail clearly and converge on `invoke.ErrNotSupported`
- we are not promising identical interactive-session behavior across all first-party providers yet

That gives us room to improve interactive support later without blocking the rest of the lifecycle work.

## Error Shape

At a high level, lifecycle behavior relies on three error categories:

- terminal command failure: non-zero exit, surfaced as `*invoke.ExitError`
- transport or provider failure: surfaced as a non-exit error
- unsupported feature: surfaced as an error and normalized to `invoke.ErrNotSupported` where that feature is not implemented

This document is not meant to capture every provider quirk. It defines the baseline that parity tests should enforce.