# Contributing

## Building and testing

Go 1.26 or newer. The repository is two modules — the root and
`docker/` — joined by a committed `go.work`, so a plain clone builds and
tests with no setup.

The recipes below need two more tools.
[`just`](https://github.com/casey/just) runs them, and comes from a
package manager (`brew install just`, `cargo install just`, or your
distribution's). `golangci-lint` does the linting and the formatting:

```sh
just tools              # installs the pinned golangci-lint
```

The pin matters. `.golangci.yml` is a version 2 configuration, so a
version 1 binary — still what several distributions package — reports
errors that are not there. `just tools` installs the version CI uses.

So does the toolchain it is built with. A golangci-lint built with an
older Go than the modules require refuses to lint them at all, which is
what a packaged binary does the day the floor moves past it. `just tools`
run from the repository builds it with the toolchain the modules ask
for, so re-run it whenever that changes. It installs into
`$(go env GOPATH)/bin`, which has to come before any packaged copy on
your `PATH`, or the packaged one still answers.

```sh
just check              # format, lint, tidy, cross-build, race tests
just test-integration   # everything needing a container runtime
```

Run `just check` before pushing. The integration lanes run the contract
suite against a real OpenSSH server and a real container and compare
the providers against each other; they need a container runtime, which
is why they are not part of `just check`.

## Repository layout

- The root is package `invoke`: the `Environment` interface, commands,
  results, and the policy layer over them — the `Executor` in
  `executor.go`, the retry mechanism behind it in `retry.go`, sudo
  wrapping in `sudo.go`, and the exported transfer policy in
  `transfer.go`.
- `local/`, `ssh/` and `fake/` are providers in the core module.
- `docker/` is the container provider, in a module of its own.
- `internal/` is mechanism more than one provider shares.
  `internal/transfer/` is the transfer engine. `internal/lifetime/` is
  the life of an `Environment`: the processes it has started and the
  transfers in flight, which `Close` ends and waits for, and the guard
  that refuses work once closing has begun. `internal/output/` stands
  between a command's output and the caller's writers.
- `invoketest/` is the contract suite every provider passes.

Every provider has the same spine, so a reader who knows one can find
their way around another: `environment.go` holds the package
documentation, the connection and its lifecycle; `process.go` a running
command; `files.go` transfers; `options.go` configuration. A concern a
target adds gets a file named for it, not a longer file. ssh has
`chain.go` (jump hosts), `keepalive.go`, `env.go` (how a command's
environment reaches it), `command.go` (the command lines it renders) and
`sftpfs.go`, the transfer engine's view of the remote side, as
`fake/vfsfs.go` is of the virtual one; docker has `shell.go`, the scripts
it runs in the container, and `place.go`, moving a download into place on
the host.

Tests run in three lanes, by build tag:

- untagged, run by `just check` (or `just test`): everything that needs
  no container runtime, the ssh provider included, against an
  in-process SSH server;
- `openssh`, run by `just test-openssh`: the ssh provider against a real
  OpenSSH server in a container;
- `docker`, run by `just test-docker`: the docker provider against a
  real container, and `TestProviderParity` (also `just test-parity`).

`just test-integration` runs both tagged lanes.

Where things go:

- Behaviour specific to one provider lives in that provider's package.
  Mechanism more than one provider shares lives under `internal/`.
- Transfer semantics are decided once. A transport that moves a file at
  a time plugs an `FS` into `internal/transfer` — local and ssh do, and
  the fake does through `fake/vfsfs.go`. A transport that moves whole
  trees, as docker's tar archives do, cannot, and decides each entry
  with `ClassifyEntry`, `EffectiveMode` and `CheckFollowTarget` instead.
- Every behavioural change lands with its contract in `invoketest` and a
  defect in `invoketest/misbehave_test.go` proving the contract can fail.

The docker provider is its own module because the Docker client library
brings a large dependency graph of its own, which consumers who never use
docker should not inherit; ssh's dependencies are small enough to stay in
the core. The docker module requires a released core version. It may
import the core's `internal/` packages for mechanism — it uses
`internal/lifetime` — but only once a release carries them, and the same
holds for anything added to the core's exported API. A shared mechanism
therefore reaches the core providers first and the docker module one
release later; until then the module keeps a copy of its own, with a
comment naming what it mirrors, and the release's repoint step is the
cue to replace it. Policy stays exported in the root package, so a
provider outside this repository can reach the same answers.
`invoketest` is public for the same reason, and the parity test lives in
`docker/` because only that module can import every provider.

Test files are named for the behaviour they cover, not for a source
file. In the root, `local/`, `ssh/`, `fake/` and `internal/`, white-box
tests (in the package itself) are named `_internal_test.go` and the rest
are in the external `_test` package. In `docker/` white-box tests carry
no suffix and are told apart by their package clause; `invoketest/`'s
tests are all white-box.

## Commits and pull requests

Commits and pull request titles are [Conventional
Commits](https://www.conventionalcommits.org): `fix(ssh): ...`,
`test(invoketest): ...`, one reviewable concern each.

A change callers have to act on carries the `!` marker — `fix(fake)!:
...`: a raised requirement, such as the Go or Docker Engine version, or
a behaviour a caller may have relied on. The marker lists it under
breaking changes in the release draft and raises the minor rather than
the patch.

A change that would stop a caller's code compiling is not one of those.
The Go API is at v1, so it needs a new major version and a new module
path, and the release draft cannot produce one. Raise it in an issue
before writing it.

Release notes are drafted automatically as pull requests merge, so a
well-titled pull request is the changelog entry.

## The convenience surface is closed

A helper earns a place on the `Executor`, or as a package-level
one-shot, only if nearly every consumer wants it, it cannot be composed
from the existing surface in a couple of obvious lines, and the
hand-rolled version hides a trap — lost diagnostics, retry-unsafe
buffering. A proposal that fails any of the three belongs in the
caller's code.

## Behavior is specified by contracts

Provider behavior is enforced by the executable suite in `invoketest`,
not by prose. Two consequences for a change:

- A fix to provider behavior lands with the contract that pins it, so
  it cannot regress silently.
- Every contract must be provably capable of failing: the suite's
  self-test runs each contract against a defect-injected provider and
  fails if any contract lacks a defect that makes it fail. A new
  contract therefore registers one in the misbehave catalog,
  `invoketest/misbehave_test.go`, demonstrating it.

An `Environment` implemented outside this repository can be verified
against the same suite with `invoketest.Verify`.

## Security

Report vulnerabilities privately — see [SECURITY.md](SECURITY.md).
