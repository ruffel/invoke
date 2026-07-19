package transfer

import (
	"fmt"
	"io/fs"

	"github.com/ruffel/invoke"
)

// Action is what a transfer should do with one directory entry, once the
// entry's type and the transfer's options have been taken together.
//
// Providers whose transport cannot drive [Copy] — one that moves whole
// trees as an archive rather than a file at a time — decide the same
// questions here, so a caller sees one set of rules whatever the target.
type Action int

const (
	// ActionCopyContent copies the entry's bytes.
	ActionCopyContent Action = iota

	// ActionPreserveLink recreates the entry as a symbolic link.
	ActionPreserveLink

	// ActionFollowLink copies the content the link resolves to, subject
	// to the containment check in [CheckFollowTarget].
	ActionFollowLink

	// ActionSkip omits the entry.
	ActionSkip
)

// Classify decides what to do with one entry. The path is used only to
// name the entry in an error, so it should be the one the caller would
// recognize.
//
// Special files — devices, sockets, named pipes — are refused by name
// unless the transfer opted to skip them, because a transfer that
// silently drops entries is indistinguishable from one that succeeded.
func Classify(path string, mode fs.FileMode, cfg invoke.TransferConfig) (Action, error) {
	switch {
	case mode.IsRegular():
		return ActionCopyContent, nil

	case mode&fs.ModeSymlink != 0:
		switch cfg.Symlinks {
		case invoke.SymlinkSkip:
			return ActionSkip, nil
		case invoke.SymlinkPreserve:
			return ActionPreserveLink, nil
		case invoke.SymlinkFollow:
			return ActionFollowLink, nil
		default:
			return ActionSkip, fmt.Errorf("unknown symlink policy %d", cfg.Symlinks)
		}

	default:
		if cfg.SkipSpecial {
			return ActionSkip, nil
		}

		return ActionSkip, fmt.Errorf(
			"unsupported special file %q (%s); use WithSkipSpecial to omit it", path, mode.Type())
	}
}

// EffectiveMode is the mode an entry should be given at the destination:
// the transfer's override when it set one, and otherwise the source's own
// permissions.
func EffectiveMode(sourceMode fs.FileMode, cfg invoke.TransferConfig) fs.FileMode {
	if cfg.Mode != nil {
		return *cfg.Mode
	}

	return sourceMode.Perm()
}

// CheckFollowTarget rejects a followed link whose target lies outside the
// transfer root. Following is the one policy that can read a path the
// caller never named, so the boundary is enforced rather than trusted.
func CheckFollowTarget(linkPath, resolved, root string, contains func(root, path string) bool) error {
	if root == "" {
		return nil
	}

	if !contains(root, resolved) {
		return fmt.Errorf("symlink %q resolves outside the transfer root: %q", linkPath, resolved)
	}

	return nil
}
