package invoke

import (
	"fmt"
	"io/fs"
)

// UnknownTotal is the TransferProgress.Total value reported when the total
// size of a transfer is not known in advance.
const UnknownTotal int64 = -1

// SymlinkPolicy selects how file transfers treat symbolic links inside a
// transferred tree.
type SymlinkPolicy int

const (
	// SymlinkPreserve recreates links as links at the destination. It is
	// the default. On targets that cannot represent links (see
	// [Capabilities].SymlinkPreserve), transfers fail with an error
	// naming the link rather than silently flattening or dropping it.
	SymlinkPreserve SymlinkPolicy = iota

	// SymlinkFollow copies the link target's content in place of the
	// link. Following never escapes the transfer root; a link pointing
	// outside it fails the transfer with an error naming the link.
	SymlinkFollow

	// SymlinkSkip omits links from the transfer.
	SymlinkSkip
)

// TransferProgress reports the progress of one file within a transfer.
//
// Callbacks may be invoked from a different goroutine than the caller's,
// depending on the provider's transport.
type TransferProgress struct {
	// Path is the file being transferred, relative to the transfer root.
	Path string

	// Current is the number of bytes transferred so far for this file.
	Current int64

	// Total is the file's total size in bytes, or [UnknownTotal] when
	// the size is not known in advance.
	Total int64
}

// TransferConfig collects the options applied to an Upload or Download.
// Providers materialize it with [NewTransferConfig]; callers use the
// With... options rather than constructing one directly.
type TransferConfig struct {
	// Mode, when non-nil, is applied to transferred files at the
	// destination (after creation, so it is umask-proof and applies on
	// overwrite too). nil preserves each source file's mode.
	Mode *fs.FileMode

	// Symlinks selects link handling; the zero value is
	// [SymlinkPreserve].
	Symlinks SymlinkPolicy

	// SkipSpecial omits FIFOs, sockets, and device files from transfers
	// instead of failing on them.
	SkipSpecial bool

	// Progress, when non-nil, receives per-file progress updates.
	Progress func(TransferProgress)
}

// TransferOption configures a single Upload or Download call.
type TransferOption func(*TransferConfig)

// NewTransferConfig applies opts over the default configuration. It is the
// entry point providers use to materialize a call's options.
func NewTransferConfig(opts ...TransferOption) TransferConfig {
	var cfg TransferConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	return cfg
}

// WithMode forces mode on transferred files at the destination, applied
// after creation so umask cannot mask it and overwrites receive it too.
// Without this option, each source file's own mode is preserved.
func WithMode(mode fs.FileMode) TransferOption {
	return func(c *TransferConfig) {
		c.Mode = &mode
	}
}

// WithSymlinks selects how symbolic links inside a transferred tree are
// handled. The default is [SymlinkPreserve].
func WithSymlinks(policy SymlinkPolicy) TransferOption {
	return func(c *TransferConfig) {
		c.Symlinks = policy
	}
}

// WithSkipSpecial omits FIFOs, sockets, and device files from a transfer.
// Without it, encountering one fails the transfer with an error naming the
// path — special files are never silently skipped, and never block a
// transfer by being opened.
func WithSkipSpecial() TransferOption {
	return func(c *TransferConfig) {
		c.SkipSpecial = true
	}
}

// WithProgress registers fn to receive per-file progress updates. See
// [TransferProgress] for the callback's concurrency caveat.
func WithProgress(fn func(TransferProgress)) TransferOption {
	return func(c *TransferConfig) {
		c.Progress = fn
	}
}

// EntryAction is what a transfer should do with one directory entry, once
// the entry's type and the transfer's options have been taken together.
//
// Deciding this is the same question for every target, and answering it
// differently is how two targets come to disagree about what a transfer
// means. Providers whose transport moves whole trees rather than a file
// at a time cannot use the shared copy engine, so they decide it here
// instead — and reach the same answers.
type EntryAction int

const (
	// CopyContent copies the entry's bytes.
	CopyContent EntryAction = iota

	// PreserveLink recreates the entry as a symbolic link.
	PreserveLink

	// FollowLink copies the content the link resolves to, subject to the
	// containment check in [CheckFollowTarget].
	FollowLink

	// SkipEntry omits the entry.
	SkipEntry
)

// ClassifyEntry decides what to do with one entry. The path is used only
// to name the entry in an error, so it should be the one a caller would
// recognize.
//
// Special files — devices, sockets, named pipes — are refused by name
// unless the transfer opted to skip them, because a transfer that
// silently drops entries cannot be told from one that succeeded.
func ClassifyEntry(path string, mode fs.FileMode, cfg TransferConfig) (EntryAction, error) {
	switch {
	case mode.IsRegular():
		return CopyContent, nil

	case mode&fs.ModeSymlink != 0:
		switch cfg.Symlinks {
		case SymlinkSkip:
			return SkipEntry, nil
		case SymlinkPreserve:
			return PreserveLink, nil
		case SymlinkFollow:
			return FollowLink, nil
		default:
			return SkipEntry, fmt.Errorf("invoke: unknown symlink policy %d", cfg.Symlinks)
		}

	default:
		if cfg.SkipSpecial {
			return SkipEntry, nil
		}

		return SkipEntry, fmt.Errorf(
			"invoke: unsupported special file %q (%s); use WithSkipSpecial to omit it", path, mode.Type())
	}
}

// EffectiveMode is the mode an entry should be given at the destination:
// the transfer's override when it set one, and otherwise the source's own
// permissions.
func EffectiveMode(sourceMode fs.FileMode, cfg TransferConfig) fs.FileMode {
	if cfg.Mode != nil {
		return *cfg.Mode
	}

	return sourceMode.Perm()
}

// CheckFollowTarget rejects a followed link whose target lies outside the
// transfer root. Following is the one policy that can read a path the
// caller never named, so the boundary is enforced rather than trusted.
//
// contains reports whether a path lies within a root, by the path rules
// of whichever side is being walked.
func CheckFollowTarget(linkPath, resolved, root string, contains func(root, path string) bool) error {
	if root == "" {
		return nil
	}

	if !contains(root, resolved) {
		return fmt.Errorf("invoke: symlink %q resolves outside the transfer root: %q", linkPath, resolved)
	}

	return nil
}
