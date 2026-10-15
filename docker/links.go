package docker

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/ruffel/invoke"
)

// maxLinkHops bounds a chain of links, as a kernel's own resolution does.
const maxLinkHops = 40

// applyLinkPolicy settles the links in a downloaded tree according to the
// transfer's symlink policy.
//
// The daemon's archive carries links as links, and an archive is read in
// one pass, so a link cannot be followed while it is being extracted: what
// it points at may come later. The tree is extracted whole into staging
// first, and the policy applied there, before anything reaches the
// caller's destination.
//
// staged is where the archive's top entry landed, and src is the path it
// came from in the container. A link's target is read in the container's
// terms, absolute or relative to the link, and a target inside src is
// found at its staged copy. One outside src is refused by name: following
// it would copy content the caller did not ask for.
func applyLinkPolicy(staged, src string, cfg invoke.TransferConfig) error {
	if cfg.Symlinks == invoke.SymlinkPreserve {
		return nil
	}

	return filepath.WalkDir(staged, func(hostPath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.Type()&fs.ModeSymlink == 0 {
			return nil
		}

		switch cfg.Symlinks {
		case invoke.SymlinkSkip:
			return os.Remove(hostPath)

		case invoke.SymlinkFollow:
			return followStagedLink(staged, src, hostPath, cfg)

		default:
			return fmt.Errorf("unknown symlink policy %d", cfg.Symlinks)
		}
	})
}

// followStagedLink replaces the link at hostPath with a copy of the file
// its chain ends at.
func followStagedLink(staged, src, hostPath string, cfg invoke.TransferConfig) error {
	rel, err := filepath.Rel(staged, hostPath)
	if err != nil {
		return err
	}

	// Where the link sits in the container, for naming it and for reading
	// a relative target against.
	linkPath := path.Join(src, filepath.ToSlash(rel))

	resolved, err := resolveStaged(staged, src, linkPath)
	if err != nil {
		return err
	}

	info, err := os.Lstat(resolved)
	if err != nil {
		return err
	}

	if !info.Mode().IsRegular() {
		return fmt.Errorf("following symlink %q: its target is not a regular file", linkPath)
	}

	mode := info.Mode().Perm()
	if cfg.Mode != nil {
		mode = *cfg.Mode
	}

	return replaceWithCopy(hostPath, resolved, mode)
}

// resolveStaged follows the chain of links starting at the container path
// linkPath, returning the staged copy of where it ends.
func resolveStaged(staged, src, linkPath string) (string, error) {
	cur := linkPath

	for range maxLinkHops {
		if !containsPath(src, cur) {
			return "", fmt.Errorf("symlink %q resolves outside the transfer root %q: %q", linkPath, src, cur)
		}

		rel, err := filepath.Rel(filepath.FromSlash(src), filepath.FromSlash(cur))
		if err != nil {
			return "", err
		}

		hostPath := filepath.Join(staged, rel)

		info, err := os.Lstat(hostPath)
		if err != nil {
			return "", fmt.Errorf("following symlink %q: %w", linkPath, err)
		}

		if info.Mode()&fs.ModeSymlink == 0 {
			// Not a link: the chain ends here.
			return hostPath, nil
		}

		target, err := os.Readlink(hostPath)
		if err != nil {
			return "", fmt.Errorf("following symlink %q: %w", linkPath, err)
		}

		if !path.IsAbs(target) {
			target = path.Join(path.Dir(cur), target)
		}

		cur = path.Clean(target)
	}

	return "", fmt.Errorf("following symlink %q: too many levels of links", linkPath)
}

// replaceWithCopy puts a copy of the file at from in place of the link at
// to, in one rename.
func replaceWithCopy(to, from string, mode fs.FileMode) error {
	in, err := os.Open(from) //nolint:gosec // Resolved inside the staging tree; resolveStaged refuses anything else.
	if err != nil {
		return err
	}

	defer func() { _ = in.Close() }()

	tmp, err := os.CreateTemp(filepath.Dir(to), ".invoke-follow-*")
	if err != nil {
		return err
	}

	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())

		return err
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())

		return err
	}

	if err := os.Chmod(tmp.Name(), mode); err != nil {
		_ = os.Remove(tmp.Name())

		return err
	}

	if err := os.Rename(tmp.Name(), to); err != nil {
		_ = os.Remove(tmp.Name())

		return err
	}

	return nil
}
