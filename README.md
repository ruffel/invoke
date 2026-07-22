# invoke

Run commands and transfer files on the local machine, remote SSH hosts, and
containers through one provider-agnostic Go interface.

```sh
go get github.com/ruffel/invoke@v0.3.0
go get github.com/ruffel/invoke/docker@v0.3.0   # only if you need containers
```

```go
env, err := local.New()          // or ssh.New(ctx, host, ...), docker.New(ctx, container, ...)
if err != nil {
    return err
}
defer env.Close()

res, stdout, _, err := invoke.NewExecutor(env).Output(ctx, invoke.New("uname", "-s"))
```

Constructing the target is the only line that changes between them.

## Design principles

- **One interface per target.** Every provider implements the same
  `Environment` contract; swapping targets does not change semantics.
- **Contracts over documentation.** Provider behavior is enforced by an
  executable suite (`invoketest`), including the failure modes:
  cancellation really terminates processes, signals really deliver,
  transfers really are atomic. The suite also tests itself — every
  contract must be provably capable of failing, or it does not merge.
- **Honest errors.** The taxonomy always distinguishes "the command ran and
  failed" from "the command could not run" from "the caller stopped it" —
  and only transport failures are ever retried.
- **POSIX targets this cycle.** Linux and macOS hosts, Linux containers.
  Everything compiles on Windows, but execution semantics are out of scope
  for now.

## Providers

| Provider | Package | Verified against |
|----------|---------|------------------|
| Local | `local` | the host, in the unit lane |
| Fake (for testing) | `fake` | the same suite as the real providers |
| SSH | `ssh` | an in-process server, and a real OpenSSH server |
| Docker | `docker` (submodule) | a real container |

Docker is a separate module, so its SDK dependency tree stays out of the
graph of anyone who does not use it.

## Capabilities

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

## Things worth knowing

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

**Docker finds its daemon the way the `docker` command does**: an endpoint
passed to `WithHost`, then `DOCKER_HOST`, then the current context. A
context reached over TLS is reported as unsupported rather than connected
to without its certificates.

**A command can outlive its own exit.** Something it left running may still
hold its output open, so `Wait` gives up after a grace period rather than
blocking forever. `local.WithTerminationGrace` sets how long that is; the
default is two seconds, which is short enough to lose the last output of a
command that flushes something substantial on the way out.

## Testing your own code

`fake` is a working target that keeps its filesystem in memory. It is not a
mock: it passes the same contract suite the real providers do, so a test
written against it cannot come to rely on behavior no real target has.

Its shell interprets a subset — sequencing, quoting, `$NAME`, `$(...)`,
redirection to `/dev/null` and between the output streams — and refuses
what it cannot run, naming it, rather than running it wrongly. A pipeline
or a `||` list fails with `ErrNotSupported` instead of quietly becoming
arguments to the first command. Script anything beyond the subset with
`Handle`, or run it against a real target.

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

## Verifying a provider of your own

`invoketest` is the suite itself, and any `Environment` implementation can
be run against it:

```go
func TestMyProvider(t *testing.T) {
    invoketest.Verify(t, func(t invoketest.T) invoke.Environment {
        return myprovider.New()
    })
}
```

## Development

```
just check              # format, lint, tidy, cross-build, race tests
just test-integration   # everything needing a container runtime
```

The integration lanes run the suite against a real OpenSSH server and a
real container, and compare the providers against each other. They need a
container runtime, so they are not part of `just check`.

Commits and pull request titles are [Conventional
Commits](https://www.conventionalcommits.org): `fix(ssh): ...`,
`test(invoketest): ...`, one reviewable concern each. A change that breaks
callers carries the `!` marker — `fix(fake)!: ...` — which is what tells
the release draft to raise the minor rather than the patch. Pre-1.0 a
breaking change is a minor; nothing here resolves to a major.

## License

MIT — see [LICENSE](LICENSE).
