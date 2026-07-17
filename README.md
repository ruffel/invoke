# invoke

Run commands and transfer files on the local machine, remote SSH hosts, and
Docker containers through one provider-agnostic Go interface.

> **Status: pre-release.** The library is under active development and the
> API is landing incrementally. There is no tagged release yet; install and
> quickstart instructions arrive with the first release.

## Design principles

- **One interface per target.** Every provider implements the same
  `Environment` contract; swapping targets does not change semantics.
- **Contracts over documentation.** Provider behavior is enforced by an
  executable contract suite (`invoketest`), including the failure modes:
  cancellation really terminates processes, signals really deliver,
  transfers are atomic.
- **Honest errors.** The taxonomy always distinguishes "the command ran and
  failed" from "the command could not run" from "the caller stopped it" —
  and only transport failures are ever retried.
- **POSIX targets this cycle.** Linux and macOS hosts, Linux containers.
  Everything compiles on Windows, but execution semantics are out of scope
  for now.

## Providers

| Provider | Package | Status |
|----------|---------|--------|
| Local | `local` | in progress |
| Fake (for testing) | `fake` | planned |
| SSH | `ssh` | planned |
| Docker | `docker` (submodule) | planned |

## License

MIT — see [LICENSE](LICENSE).
