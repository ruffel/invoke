# Contributing

## Building and testing

Go 1.25 or newer. The repository is two modules — the root and
`docker/` — joined by a committed `go.work`, so a plain clone builds and
tests with no setup.

```sh
just check              # format, lint, tidy, cross-build, race tests
just test-integration   # everything needing a container runtime
```

Run `just check` before pushing. The integration lanes run the contract
suite against a real OpenSSH server and a real container and compare
the providers against each other; they need a container runtime, which
is why they are not part of `just check`.

## Commits and pull requests

Commits and pull request titles are [Conventional
Commits](https://www.conventionalcommits.org): `fix(ssh): ...`,
`test(invoketest): ...`, one reviewable concern each.

A change that breaks callers carries the `!` marker — `fix(fake)!: ...`.
The marker is what tells the release draft to raise the minor rather
than the patch; pre-1.0 a breaking change is a minor, and nothing here
resolves to a major.

Release notes are drafted automatically as pull requests merge, so a
well-titled pull request is the changelog entry.

## Behavior is specified by contracts

Provider behavior is enforced by the executable suite in `invoketest`,
not by prose. Two consequences for a change:

- A fix to provider behavior lands with the contract that pins it, so
  it cannot regress silently.
- Every contract must be provably capable of failing: the suite's
  self-test runs each contract against a defect-injected provider and
  fails if any contract lacks a defect that makes it fail. A new
  contract therefore registers one in the misbehave catalog
  demonstrating it.

An `Environment` implemented outside this repository can be verified
against the same suite with `invoketest.Verify`.

## Security

Report vulnerabilities privately — see [SECURITY.md](SECURITY.md).
