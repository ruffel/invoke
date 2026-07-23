# invoke

[![Go Reference](https://pkg.go.dev/badge/github.com/ruffel/invoke.svg)](https://pkg.go.dev/github.com/ruffel/invoke)
[![CI](https://github.com/ruffel/invoke/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/ruffel/invoke/actions/workflows/ci.yml?query=branch%3Amain)

Run commands and transfer files on the local machine, remote SSH hosts, and
containers through one provider-agnostic Go interface.

## Features

- **One interface, four targets.** `local`, `ssh`, `docker`, and an
  in-memory `fake` all satisfy the same `Environment` contract; the
  constructor is the only line that changes.
- **Contract-verified behavior.** An executable suite enforces the
  semantics against every provider — cancellation really terminates,
  signals really deliver, transfers land whole or not at all — including
  against a real OpenSSH server and a real container on every change. The
  suite tests itself: a contract that cannot be made to fail does not
  merge.
- **Retry-safe by construction.** The error taxonomy separates "ran and
  failed" from "could not run" from "the caller stopped it", and only
  transport failures — the one family that says the command did not run —
  are ever retried. A retry can never run a side-effectful command twice.
- **A test double that cannot drift.** The fake passes the same contract
  suite as the real providers, so tests written against it cannot come to
  rely on behavior no real target has.

## Installation

```sh
go get github.com/ruffel/invoke
go get github.com/ruffel/invoke/docker   # only if you need containers
```

Docker is a separate module, so its SDK dependency tree stays out of the
graph of anyone who does not use it.

## Quick start

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/local"
)

func main() {
	env, err := local.New()
	if err != nil {
		log.Fatal(err)
	}
	defer env.Close()

	_, out, _, err := invoke.NewExecutor(env).Output(
		context.Background(), invoke.New("uname", "-s"))
	if err != nil {
		log.Fatal(err)
	}

	fmt.Print(string(out))
}
```

An `Environment` is a connection to a target; an `Executor` wraps one and
adds the policy most callers want — output capture, retries, sudo. A
`Command` is a plain value with no streams and no state, so the same one
runs repeatedly or against several targets; where output goes belongs to
the invocation, not the command. Transfers ride the same environment:
`exec.Upload(ctx, "./dist", "/srv/app")` copies a file or a whole tree.

To run the same command elsewhere, construct a different target. A remote
host authenticates with a key file, the agent (`ssh.WithAgent`), or a
password, and verifies the host key against the file you name — there is
no trust-on-first-use:

```go
env, err := ssh.New(ctx, "build-3.example.net",
	ssh.WithUser("deploy"),
	ssh.WithPrivateKey(home+"/.ssh/id_ed25519"),
	ssh.WithKnownHosts(home+"/.ssh/known_hosts"),
)
```

A container is named by name or ID, and the daemon is found the way the
`docker` command finds it — `WithHost`, a named context, `DOCKER_HOST`,
then the current context:

```go
env, err := docker.New(ctx, "build-cache")
```

Runnable examples for every concept — retries, capabilities, the fake,
transfers — are on
[pkg.go.dev](https://pkg.go.dev/github.com/ruffel/invoke#pkg-examples).

## Handling failure

Every failure answers one question first: did the command run? Branch on
the kind, never the message:

```go
_, err := exec.Run(ctx, invoke.Shell("systemctl restart app"), invoke.IO{})

var exitErr *invoke.ExitError

switch {
case err == nil: // ran and exited zero
case errors.As(err, &exitErr): // ran and failed; the exit status is trustworthy
	log.Printf("restart failed with exit %d", exitErr.Code)
case errors.Is(err, invoke.ErrNotFound): // could not start: no such executable
default: // everything else, including transport — the only family retries re-run
	log.Print(err)
}
```

`invoke.WithRetry(3, invoke.ExponentialBackoff(time.Second))` re-runs
transport failures and nothing else; the reasoning is under
[Caveats](#caveats).

## Providers

| Provider | Package | Verified against |
|----------|---------|------------------|
| Local | `local` | the host, in the unit lane |
| Fake (for testing) | `fake` | the same suite as the real providers |
| SSH | `ssh` | an in-process server, and a real OpenSSH server |
| Docker | `docker` (submodule) | a real container |

### Capabilities

Targets differ in what they can do and say so through `Capabilities()`.
The rule is symmetric and the suite enforces both halves: a declared
capability must work, and an undeclared one must fail with
`ErrNotSupported` rather than being quietly ignored.

| Capability | local | ssh | docker | fake |
|-------------------|:-----:|:---:|:------:|:----:|
| `TTY`             | yes   | yes | yes    | no   |
| `Signals`         | yes   | yes² | yes¹   | yes  |
| `SymlinkPreserve` | yes   | yes | yes    | yes  |

¹ A container must have a shell for a signal to reach the right process.
Without one the capability is not declared.

² SSH carries the signal but offers no confirmation, so a server that
discards it cannot be told from one that acted. The servers this is
tested against — a real OpenSSH server, and the in-process one — honor
it. For any other, run `invoketest` against it and the signal contracts
will answer.

A signal always reaches the process. Whether it reaches that process's
children depends on the target: local runs commands in their own process
group and signals the group, as a terminal does; ssh and docker address
the single process.

## Testing your own code

`fake` is a working target that keeps its filesystem in memory. It is not a
mock: it passes the same contract suite the real providers do, so a test
written against it cannot come to rely on behavior no real target has.

Its shell interprets a subset — sequencing with `;` and `&&`, quoting,
`$NAME` and `${NAME}`, `$(...)` substitution, redirection to `/dev/null`
and between the output streams — and refuses what it cannot run, naming
it, rather than running it wrongly. A pipeline, a `||` list, a glob, a
comment: each fails with `ErrNotSupported` instead of quietly meaning
something else, and the builtins answer truthfully or refuse loudly.
Script anything beyond the subset with `Handle`, or run it against a real
target.

```go
env := fake.New()
env.Handle("systemctl", func(_ context.Context, cmd invoke.Command, s *fake.Session) int {
    return 0
})

// ... exercise your code against env ...

for _, call := range env.Calls() {
    // assert on what your code asked to run
}
```

Any `Environment` implementation outside this repository can be held to
the same contracts:

```go
func TestMyProvider(t *testing.T) {
    invoketest.Verify(t, func(t invoketest.T) invoke.Environment {
        return myprovider.New()
    })
}
```

## Caveats

Each of these is a real constraint of the target rather than a limitation
of the API, and each is covered by a test.

**Retries only re-run transport failures.** A connection dying under a
running command is a transport failure by any ordinary reading, but the
command may already have taken effect and nothing on this side can tell.
Retrying would run an arbitrary command a second time, so it is terminal
and the error says so. File transfers are the exception, and only because
they are built to be: each is delivered whole or not at all.

**SSH environment variables need somewhere to go.** A server accepts only
the variables its `AcceptEnv` setting names, and the stock setting names
none. Refused variables travel in a file only the login user can read,
which the command line sources and deletes before the command runs, so
values never appear in a remote process table. `WithCommandLineEnv` puts
them on the command line instead, where every account on the host can read
them.

**Docker needs a shell in the container** for signal delivery and for
staging transfers. Without one, both report `ErrNotSupported` rather than
misbehaving.

**A command can outlive its own exit.** Something it left running may still
hold its output open, so `Wait` gives up after a grace period rather than
blocking forever. `local.WithTerminationGrace` sets how long that is; the
default is two seconds, which is short enough to lose the last output of a
command that flushes something substantial on the way out.

## Stability

Pre-1.0: breaking changes land as minor releases and every one is called
out in the [release notes](https://github.com/ruffel/invoke/releases).
POSIX targets this cycle — Linux and macOS hosts, Linux containers.
Everything compiles on Windows, but execution semantics are out of scope
for now. Go 1.25 or newer.

## Contributing

```
just check              # format, lint, tidy, cross-build, race tests
just test-integration   # everything needing a container runtime
```

The integration lanes run the suite against a real OpenSSH server and a
real container, and compare the providers against each other. Conventions
for commits, pull requests, and behavioral changes are in
[CONTRIBUTING.md](CONTRIBUTING.md).

## License

MIT — see [LICENSE](LICENSE).
