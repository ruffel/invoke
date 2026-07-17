// Package invoke runs commands and transfers files on execution targets —
// the local machine, remote hosts over SSH, and Docker containers — through
// one provider-agnostic interface.
//
// Providers live in subpackages and are verified against a shared behavioral
// contract suite so that swapping targets does not change semantics. The
// public API is landing incrementally; see the README for current status.
package invoke
