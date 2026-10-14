# Findings, October 2026, and the order to fix them

Status: **open**, written 2026-10-14. Every finding below was re-checked
against `main` at 2a53681 on that date; the September audit's line
numbers were not, and should be confirmed before acting on them.

This covers the correctness bugs found after `architecture-audit.md` was
written. That document's 49-item plan still stands; what changes here is
the order, because these bugs sit ahead of the structural work they were
found alongside.

## Why the order is what it is

Three bodies of work are in flight and none can be finished where they
sit. The September audit left 46 open items. A re-audit on 2026-10-13
found eight correctness bugs and was never written down, which is what
this document fixes. And the nightly stress job has failed 15 of its last
40 runs without being able to say which test failed, so nothing above it
could be verified honestly.

So: make the nightly legible, write the record down, fix behaviour, then
resume structure.

## Findings

R = reproduced by experiment on 2026-10-13. C = confirmed by reading the
code on 2026-10-14. Severity is caller impact, not effort.

| # | Finding | Evidence | Severity |
|---|---|---|---|
| 1 | `Executor.Lines` hangs forever on a line over 1 MiB, past its context deadline. The scanner goroutine returns without closing the read end, so the provider's copier blocks writing to a pipe nobody reads and `Wait` never returns. | R + C `executor.go` streamOnce | High |
| 2 | A transfer rooted at `/` fails every entry. `Contains(root, path)` is `path == root \|\| HasPrefix(path, root+"/")`, which for root `/` compares against `//`. | R + C `internal/transfer/hostfs.go:38-40`, same shape in `sftpFS` | High |
| 3 | `ssh.Environment.Close` hangs on a silently dead link when a process is running: it stops the keepalive, waits on each process, and closes the chain last. The wait it depends on can only end once the transport is gone. | R + C `ssh/environment.go:155-185` | High |
| 4 | A cancelled ssh transfer never returns when the SFTP server stalls. `sftpSession.close` calls `client.Close()` before `session.Close()`, and the client's close is itself a round trip to a server that has stopped answering. | R + C `ssh/files.go:324-344` | High |
| 5 | The ssh env file, which carries the caller's secrets, is left in `/tmp` when the caller's `PATH` has no `rm`. The prologue that removes it runs under the caller's `PATH`; the detached sweep only covers outcomes that are not an `ExitError`. | R | High |
| 6 | docker returns a retryable `TransportError{wait}` whenever a stream copy errored, discarding a real exit status from the daemon. `WithRetry` then re-runs a command that definitely ran, which is the one thing the retry rule exists to prevent. A caller's own failing writer is enough to trigger it. | C `docker/process.go:440-455` | High |
| 7 | `PATH` in `Command.Env` means three different things. local ignores it for resolution (Go resolves `cmd.Path` against the parent's `PATH`), ssh interpolates it into the pre-check and honours it, docker's pre-flight ignores it. No contract covers this. | R + C `local/process.go:74-81`, `ssh/command.go:133-138` | Medium |
| 8 | docker, in a shell-less container: `Close` and cancellation block until the command exits on its own; `Download` is refused for want of a shell it never uses; `Upload` replaces an existing destination directory where the engine merges. | C | Medium |
| 9 | The contract suite proves transfer policy through `Upload` only: 16 upload defects against 1 download. That is why docker's and the fake's `Download` both ignore `WithSymlinks` unnoticed. | C | Medium |
| 10 | `local` never calls `invoketest.Verify` from its own package, so the reference provider is the one not held to the published spec where anyone would look for it. | C | Low |
| 11 | flake-watch fails often and cannot name the failing test: a failing integration test prints the server's debug log, which is longer than the 120 lines the job tails, and the logs are never kept. | C | High |
| 12 | `lifecycle/close-is-idempotent` flakes in the openssh lane. Three occurrences by 2026-09-04, cause unknown, not reproducible locally in 540 targeted attempts. Blocked on 11. | none yet | Medium |
| 13 | Dependabot #76 has been red since 2026-09-11: it raises the `go` directive to 1.26.0 in both modules but cannot touch `go.work`, so the workspace check added in #73 fails it. | C | Medium |

#80 merged on 2026-10-14 and pins the current shell-less refusals as
tests, so narrowing them (part of 8) now means changing a test merged the
same day. That is a decision, not a fix.

## Decisions

| ID | Question | Recommendation |
|---|---|---|
| D2 | Take Dependabot #76, which raises the Go floor to 1.26? **This is forced, not incidental**: x/crypto v0.56.0 and later and x/sys v0.48.0 all declare `go 1.26.0`. Holding the floor means holding x/crypto at v0.55.0, on a library whose SSH provider is built on it. | Raise the floor deliberately, in its own PR, moving `go.mod` in both modules, `go.work`, README's "Go 1.25 or newer" and the release notes together. The alternative, pinning x/crypto back and telling Dependabot to ignore it, trades a stated minimum for stale crypto, which is the worse of the two. |
| D3 | What does `PATH` in `Command.Env` mean? | ssh's reading: the caller's `PATH` resolves the command. Write it as a contract, then make local and docker match. The alternative makes `Command.Env` a lie. |
| D4 | docker shell-less: keep the refusals #80 pinned, or narrow them? | Narrow `Download`, which uses no shell; keep the rest; fix the `Close` and cancel hang regardless, since that is a hang rather than a policy. |
| D5 | What is the next release? Core has had no tag since v1.1.0, and #81 raises the docker module's daemon requirement to 19.03. | A daemon requirement is not a Go API break, so v1.2.0 for both is defensible if the release notes say so. Fix the versioning documents (audit item 7) first: they still describe a pre-1.0 policy. D2's outcome belongs in the same notes. |

## Order of work

### Phase 0, make the nightly legible and save the record

1. `ci: Name the test that failed in a flake-watch run`, which closes 11
   and unblocks 12.
2. The Dependabot question, per D2, closes 13.
3. `docs: Track the design documents and the October findings`: this
   document and the three it sits beside. Audit item 16, pulled forward.

### Phase 1, correctness, no API change

One branch and one conventional commit each, with a test proven to fail
first. All six are independent.

4. `fix(invoke): Fail a too-long line instead of hanging`, for finding 1.
5. `fix(docker): Keep a reported exit status over a stream copy error`, for finding 6.
6. `fix(ssh): Close the session before the client so a stalled transfer unwinds`, for finding 4.
7. `fix(ssh): Sever the connection before waiting on processes in Close`, for finding 3.
8. `fix(ssh): Remove the env file without depending on the caller's PATH`, for finding 5.
9. `fix(transfer): Handle a transfer rooted at the filesystem root`, for finding 2.

Items 4, 5 and 9 want contracts as well as unit tests, being
provider-independent promises. Two open September items are small enough
to fold in here: item 2 (`fake` track re-check) and item 3 (`fake` sh
spelling).

### Phase 2, decided behaviour

Needs D3 and D4 first.

10. `test(invoketest): State what PATH in Command.Env means`.
11. `fix(local): Resolve a command against the caller's PATH`.
12. `fix(docker): Unblock Close and cancellation without a shell`.

### Phase 3, close the gap that let the drift happen

13. `test(invoketest): Prove download-side policy`, for finding 9; audit item 35.
14. `test(local): Run the contract suite from the package`, for finding 10; audit item 42.

### Phase 4, release

15. `docs: Bring the versioning policy to post-1.0`, audit item 7.
16. `build(just): Encode the repoint step`, audit item 14.

Then tag per D5, repointing `docker/go.mod` first.

### Phase 5, the structural backlog

The September plan's remaining items in its own order, roughly 40 PRs,
none urgent.

## Totals

Through Phase 4: 16 to 18 PRs. The long tail after that is the September
plan, unchanged.

## What this does not do

It does not fix 12. That is not fixable until it can be diagnosed, which
is what Phase 0 item 1 buys. The next occurrence after that lands should
name the test and carry the server's account of the exchange.
