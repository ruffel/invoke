package docker

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/ruffel/invoke"
)

// maxLinkHops bounds a chain of links, as a kernel's own resolution does.
const maxLinkHops = 40

// linkSettler applies the transfer's symlink policy to a downloaded tree,
// if the policy asks for anything to be done.
//
// The daemon's archive carries links as links, and an archive is read in
// one pass, so a link cannot be followed while it is being extracted: what
// it points at may come later. The tree is extracted whole into staging
// first and settled there, before anything reaches the caller's
// destination.
//
// staged is where the archive's top entry landed, and src is the path it
// came from in the container.
func linkSettler(staged, src string, cfg invoke.TransferConfig) settleFunc {
	if cfg.Symlinks == invoke.SymlinkPreserve {
		return nil
	}

	return func(ctx context.Context, fileModes map[string]fs.FileMode) error {
		return applyLinkPolicy(ctx, staged, src, cfg, fileModes)
	}
}

// applyLinkPolicy removes or follows every link in the staged tree.
//
// A link's target is read in the container's terms, absolute or relative
// to the link, and a target inside src is found at its staged copy. One
// outside src is refused by name: following it would copy content the
// caller did not ask for.
func applyLinkPolicy(
	ctx context.Context,
	staged, src string,
	cfg invoke.TransferConfig,
	fileModes map[string]fs.FileMode,
) error {
	if info, err := os.Lstat(staged); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("the transfer root %q is itself a symbolic link", src)
	}

	return filepath.WalkDir(staged, func(hostPath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if err := ctx.Err(); err != nil {
			return err
		}

		if entry.Type()&fs.ModeSymlink == 0 {
			return nil
		}

		switch cfg.Symlinks {
		case invoke.SymlinkSkip:
			return os.Remove(hostPath)

		case invoke.SymlinkFollow:
			return followStagedLink(ctx, staged, src, hostPath, cfg, fileModes)

		default:
			return fmt.Errorf("unknown symlink policy %d", cfg.Symlinks)
		}
	})
}

// followStagedLink replaces the link at hostPath with a copy of the file
// its chain ends at.
func followStagedLink(
	ctx context.Context,
	staged, src, hostPath string,
	cfg invoke.TransferConfig,
	fileModes map[string]fs.FileMode,
) error {
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

	// The followed copy carries the mode its target will: the override
	// when there is one, else the target's own, which may still be held
	// back while the tree is settled.
	mode := info.Mode().Perm()
	if final, ok := fileModes[resolved]; ok {
		mode = final
	}

	if cfg.Mode != nil {
		mode = *cfg.Mode
	}

	var reader io.Reader

	in, err := os.Open(resolved) //nolint:gosec // Resolved inside the staging tree; resolveStaged refuses anything else.
	if err != nil {
		return err
	}

	defer func() { _ = in.Close() }()

	reader = &ctxReader{ctx: ctx, inner: in}
	if cfg.Progress != nil {
		reader = &progressReader{inner: reader, path: filepath.ToSlash(rel), total: info.Size(), fn: cfg.Progress}
	}

	if err := replaceWithCopy(hostPath, reader); err != nil {
		return err
	}

	fileModes[hostPath] = mode

	return nil
}

// resolveStaged follows the container path linkPath to the file it ends
// at, returning that file's staged copy.
//
// The path is resolved a component at a time, as the kernel resolves
// one, so a link anywhere along it is followed in the container's terms
// rather than by the host. Mapping a whole path into staging and asking
// the host about it would let a link standing in a parent component take
// the lookup out of the staged tree, and onto whatever the host holds at
// the path it names. Every component is checked to stay inside src
// before anything is asked of it.
func resolveStaged(staged, src, linkPath string) (string, error) {
	r := stagedResolver{staged: staged, src: src, linkPath: linkPath}

	rel, err := r.relative(linkPath)
	if err != nil {
		return "", err
	}

	cur := src
	queue := splitPath(rel)
	hops := 0

	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]

		next, linkTarget, err := r.step(cur, name, len(queue) > 0)
		if err != nil {
			return "", err
		}

		if linkTarget == "" {
			cur = next

			continue
		}

		hops++
		if hops > maxLinkHops {
			return "", fmt.Errorf("following symlink %q: too many levels of links", linkPath)
		}

		cur, queue, err = r.enter(cur, linkTarget, queue)
		if err != nil {
			return "", err
		}
	}

	return r.stage(cur)
}

// stagedResolver holds what one resolution needs to name its errors and
// to find container paths in staging.
type stagedResolver struct {
	staged, src, linkPath string
}

func (r stagedResolver) outside(at string) error {
	return fmt.Errorf("symlink %q resolves outside the transfer root %q: %q", r.linkPath, r.src, at)
}

// relative returns a contained container path relative to src.
func (r stagedResolver) relative(containerPath string) (string, error) {
	rel, err := filepath.Rel(filepath.FromSlash(r.src), filepath.FromSlash(containerPath))
	if err != nil {
		return "", err
	}

	return filepath.ToSlash(rel), nil
}

// stage finds a contained container path's staged copy.
func (r stagedResolver) stage(containerPath string) (string, error) {
	rel, err := r.relative(containerPath)
	if err != nil {
		return "", err
	}

	return filepath.Join(r.staged, filepath.FromSlash(rel)), nil
}

// step resolves one component name below the resolved directory cur. It
// returns where that leads, and the link's target when it is a link.
// more says whether components remain, which only a directory can have.
func (r stagedResolver) step(cur, name string, more bool) (string, string, error) {
	switch name {
	case "", ".":
		return cur, "", nil
	case "..":
		up := path.Dir(cur)
		if !containsPath(r.src, up) {
			return "", "", r.outside(up)
		}

		return up, "", nil
	}

	next := path.Join(cur, name)
	if !containsPath(r.src, next) {
		return "", "", r.outside(next)
	}

	hostPath, err := r.stage(next)
	if err != nil {
		return "", "", err
	}

	// cur is resolved and its staged copy is a real directory, so this
	// asks about one entry in it and nothing beyond.
	info, err := os.Lstat(hostPath)
	if err != nil {
		return "", "", fmt.Errorf("following symlink %q: %w", r.linkPath, err)
	}

	if info.Mode()&fs.ModeSymlink == 0 {
		if more && !info.IsDir() {
			return "", "", fmt.Errorf("following symlink %q: %q is not a directory", r.linkPath, next)
		}

		return next, "", nil
	}

	target, err := os.Readlink(hostPath)
	if err != nil {
		return "", "", fmt.Errorf("following symlink %q: %w", r.linkPath, err)
	}

	return next, target, nil
}

// enter continues the resolution through a link's target: from the root
// for an absolute one, from the link's own directory for a relative one.
func (r stagedResolver) enter(cur, target string, queue []string) (string, []string, error) {
	if !path.IsAbs(target) {
		return cur, append(splitPath(target), queue...), nil
	}

	clean := path.Clean(target)
	if !containsPath(r.src, clean) {
		return "", nil, r.outside(clean)
	}

	rel, err := r.relative(clean)
	if err != nil {
		return "", nil, err
	}

	return r.src, append(splitPath(rel), queue...), nil
}

// splitPath breaks a slash-separated path into its components.
func splitPath(p string) []string {
	if p == "" || p == "." {
		return nil
	}

	return strings.Split(p, "/")
}

// replaceWithCopy puts the content read from r in place of the link at
// to, in one rename. It is left readable and writable by its owner; the
// mode it is to carry is applied with the rest of the tree's.
func replaceWithCopy(to string, r io.Reader) error {
	tmp, err := os.CreateTemp(filepath.Dir(to), ".invoke-follow-*")
	if err != nil {
		return err
	}

	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())

		return err
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())

		return err
	}

	if err := os.Rename(tmp.Name(), to); err != nil {
		_ = os.Remove(tmp.Name())

		return err
	}

	return nil
}
