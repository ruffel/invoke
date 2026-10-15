# Security policy

## Reporting a vulnerability

Please report suspected vulnerabilities privately, through GitHub's
[private vulnerability reporting][advisory] — open the repository's
**Security** tab and choose **Report a vulnerability**. This keeps the
report confidential until a fix is available.

Please do not open a public issue for a security report.

A report is most useful when it includes the affected version, the
provider it concerns (local, ssh, or docker), and the smallest set of
steps that reproduces the behaviour. You can expect an acknowledgement
within a few days.

[advisory]: https://github.com/ruffel/invoke/security/advisories/new

## Supported versions

Fixes land on the latest release, as a new patch or minor version; an
earlier release is never patched in place. There is no long-term
support for earlier versions: upgrading to the latest release is the
way to receive a fix.

## Scope

This library runs commands and moves files on targets the caller names —
the local machine, a remote host over SSH, a container. Two boundaries
are worth stating, because they shape what counts as a vulnerability:

- **The command is the caller's.** The API runs what it is given, with no
  shell interpretation unless [`Shell`] is used, in which case quoting is
  the caller's responsibility. A command that harms its own target is not
  a vulnerability in the library.
- **The target may be untrusted; the caller's machine is defended from
  it.** A file transfer must not let a hostile target write outside the
  destination the caller chose, and an interrupted command must not be
  reported as one that certainly ran. Failures of that kind are in scope.
- **The Docker daemon is trusted infrastructure.** The docker provider is
  a client of a daemon the caller chose to talk to; a vulnerability in the
  daemon itself is the daemon's to fix, and the exposure tracks the
  installed Docker Engine version rather than anything this library pins.
  The provider depends on the client and the API types alone, which are
  published as their own modules — `github.com/moby/moby/client` and
  `github.com/moby/moby/api` — apart from the daemon, so an advisory
  against daemon code names a module this library does not require.

[`Shell`]: https://pkg.go.dev/github.com/ruffel/invoke#Shell
