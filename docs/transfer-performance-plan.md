# Transfer performance: PR and commit plan

2026-08-28. Breaks the SFTP transfer performance work into independently
landable PRs, each a small number of conventional commits, ordered so the
tree is green and every claim is measured at each step.

Related:

- The pipelining issue (drafted, to file): sequential 32 KiB round trips
  cap throughput at ~55 MB/s; covers PRs 2–3.
- A companion issue for parallel tree copies (to draft): covers PR 4.
- Skip-unchanged transfers: separate roadmap item with its own design doc
  pending. Out of scope here; noted under Future because the three
  compose: pipelining wins first-copy bandwidth, parallel copies win
  first-copy trees, skip-unchanged wins every copy after the first.

## Context

Transfer cost has three independent axes, and each PR addresses exactly
one:

| Workload           | Binding constraint            | Addressed by |
| ------------------ | ----------------------------- | ------------ |
| One large file     | Bytes per round trip          | PR 2, PR 3   |
| Tree of many files | Round trips per file (~6)     | PR 4         |
| Repeated syncs     | Bytes that didn't need moving | skip-unchanged (separate) |

Mechanism, verified against pkg/sftp v1.13.11: the client in
`ssh/files.go` is constructed bare, so both directions move one 32 KiB
request per round trip. `UseConcurrentWrites(true)` alone is a null
change — `ReadFrom` sizes its pipeline by sniffing the reader for
`Len`/`Size`/`*io.LimitedReader`/`Stat`, and the engine's
`ctxReader`/`progressReader` wrappers expose none of them, so the size
guess is zero and the sequential loop runs anyway. Downloads never reach
`WriteTo` (the pipelined read path, on by default) because the source is
wrapped before `io.Copy` can see it implements `io.WriterTo`.

## Sequencing

1. Benchmarks first, so every later PR carries before/after numbers from
   this repo rather than from a standalone client.
2. Upload pipelining (smallest risk, largest single-file win).
3. Download pipelining (engine-only; no client option needed).
4. Parallel tree copies (real design; issue first, per design-doc-first).

Every PR: `just check` green, invoketest contract suite unchanged unless
a contract is being deliberately extended.

---

## PR 1 — Transfer benchmarks

Branch: `bench/transfer-throughput`

Measurement infrastructure only; no behavior change. Reuses the existing
dockerized sshd from `ssh/openssh_test.go`.

Commits:

1. `refactor(ssh): Accept testing.TB in the container helpers`
   — `startContainer` and friends currently take `*testing.T`; widen so
   benchmarks can share them. Mechanical, no test changes.
2. `test(ssh): Benchmark transfers against a real sshd`
   — `BenchmarkUpload` / `BenchmarkDownload` over two shapes: one 256 MiB
   file (throughput) and a 2,000 × 4 KiB tree (files/second). Reports
   MB/s and files/s via `b.ReportMetric`. Gated exactly like the
   integration tests (skipped when docker is unavailable).

Gate: benchmarks run and reproduce the known baseline (~55 MB/s class on
loopback; single-digit ms per small file). Record the numbers in the PR
description — they are the baseline every later PR quotes.

Risk: none (test-only).

## PR 2 — Upload pipelining

Branch: `perf/sftp-upload-pipelining` · closes the pipelining issue's
upload half.

Commits:

1. `perf(transfer): Carry the stat'ed source size through the copy readers`
   — `writeFile` already receives the source size (`total`); wrap the
   final reader in a `sizedReader` exposing `Size() int64` so the size
   survives the ctx/progress wrapping. Inert on its own: the concurrent
   path is still gated off client-side. Includes the white-box guard that
   the value handed to `io.Copy` exposes the size — this regression is
   otherwise invisible (everything passes, just 5× slower).
   `Size()` picks concurrency; it must never truncate — deliberately not
   `io.LimitedReader`, so a source that grows mid-copy still copies to
   EOF.
2. `perf(ssh): Enable concurrent SFTP writes`
   — `sftp.NewClientPipe(stdout, stdin, sftp.UseConcurrentWrites(true))`
   plus a comment carrying the safety argument. Commit body records the
   measured before/after. Packet size stays 32 KiB (OpenSSH drops
   oversized messages; the protocol guarantees only 32 KiB — measured
   connection loss at 256 KiB).

Safety (also in the issue): the documented pkg/sftp hazard — holes in the
file on mid-transfer error — is confined to a temp file the engine
already discards. `copyFile` writes to an exclusively created temp,
removes it on failure, renames on success; the contract suite pins
"destination unchanged after a failed transfer". Remote temp is opened
`O_WRONLY|O_CREATE|O_EXCL`, regular files only, no append mode. Progress
stays monotonic: `ReadFrom`'s slicer reads the source from one goroutine.

Gate: loopback upload ≥ ~5× baseline (expect ~280 MB/s class); small-tree
benchmark unchanged (files ≤ one packet keep the single-shot path);
contract suite untouched and green.

Risk: effectively none user-visible; in-flight memory is bounded at
64 × 32 KiB = 2 MiB per file.

## PR 3 — Download pipelining

Branch: `perf/sftp-download-pipelining` · closes the pipelining issue.

Commit:

1. `perf(transfer): Route copies through the source's WriteTo when it has one`
   — in `writeFile`, when the source implements `io.WriterTo`, move the
   ctx/progress wrapping to the destination writer (new `ctxWriter` /
   `progressWriter`) and drive the copy with the bare source, so
   `(*sftp.File).WriteTo` engages: it fstats its own handle and pipelines
   reads (concurrent reads are on by default — no client change needed).
   `WriteTo` delivers strictly in order, so per-chunk progress and
   cancellation are preserved. Local→local keeps today's shape
   (`*os.File` is not a WriterTo). White-box guard mirrors PR 2's: a
   WriterTo source must be handed over bare.

Gate: loopback download ≥ ~5× baseline; the canceled-download and
failed-transfer atomicity contracts (already in invoketest) green.

Risk: cancellation checks move from the read side to the write side —
same per-chunk granularity, and the existing contracts cover exactly
this observable.

## PR 4 — Parallel tree copies

Branch: `perf/parallel-tree-copy` · needs its companion issue (and a
short design pass) first.

Small files never benefit from PRs 2–3: their cost is the ~6-round-trip
metadata chain (open, chmod, write, fsync, close, rename), serialized
per file by the walk. At 20 ms RTT that is ~8 files/s. The fix is
copying N files concurrently over the one SFTP session — pkg/sftp
multiplexes requests by ID and is safe for concurrent use.

Commits (shape; the issue pins the open decisions):

1. `feat: Add WithConcurrency transfer option`
   — public `TransferOption`; `1` means today's strictly sequential
   behavior. Default when unset: the FS's advertised preference (lean: a
   small optional interface the sftp FS implements to request ~8, host
   FS stays 1 — local behavior unchanged by default, remote fast by
   default).
2. `perf(transfer): Copy tree entries through a bounded worker pool`
   — the walk stays single-goroutine (directory creation order and the
   deferred dir-chmod pass are unchanged); regular-file copies fan out
   to the pool; first error cancels the rest; per-file temps already
   clean up on failure. Documents on `TransferProgress` that per-file
   events may now interleave across files (`Path` disambiguates; the
   cross-goroutine caveat is already documented).
3. `test(invoketest): Pin transfer semantics under concurrency`
   — contracts: atomicity under a mid-tree failure with workers > 1;
   progress events demultiplex by Path; `WithConcurrency(1)` is
   byte-identical to today.

Gate: small-tree benchmark ≥ ~4× on loopback with the default workers
(expect ~worker-count× over real latency); single-large-file numbers
unchanged; contract suite green including the new contracts.

Risk: progress interleaving is a new observable (documented, opt-out via
`WithConcurrency(1)`); first-error reporting becomes timing-dependent
among concurrent failures; in-flight memory is workers × 2 MiB.

---

## Release

All four land in a minor release (target `v1.2.0`): PRs 2–3 are perf,
PR 4 adds API. Per RELEASING.md's two-stage tagging — and two known
traps from last time: bump the docker module's core pin after tagging
(it was skipped in v1.0.0), and don't trust RELEASING.md's own verify
snippet (broken).

## Rejected / deferred

- **Bigger packets** (`MaxPacketUnchecked`): measured connection loss
  against OpenSSH at 256 KiB; out of spec. Rejected in the issue.
- **Raising `MaxConcurrentRequestsPerFile` past 64**: pointless — the
  SSH channel window is the same ~2 MiB (hardcoded in x/crypto/ssh), so
  the WAN ceiling is ~2 MiB/RTT (~21 MB/s at 100 ms) regardless. Stock
  OpenSSH tooling shares the ceiling; not a competitive gap.
- **Striping one file across multiple SSH channels**: the only way past
  that ceiling; heavy machinery, out of character. Revisit only on a
  real report.
- **Per-call SFTP session reuse** (~3 round trips per Upload/Download
  call): deliberate design — per-call teardown is what makes
  cancellation work. Guidance instead: transfer trees per call, not a
  file per call.
- **Env delivery cost** (up to two extra sessions per Run when sshd
  refuses Setenv): `CommandLineEnv` is the documented trade; doc note at
  most.
- **Skip-unchanged**: separate design doc, pending.
