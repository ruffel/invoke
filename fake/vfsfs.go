package fake

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"strings"

	"github.com/ruffel/invoke/internal/transfer"
)

// vfsFS presents the virtual filesystem to the shared transfer engine, so
// the fake's transfers are the engine's transfers: the same per-file
// atomicity, merge, symlink policy, containment, progress and concurrency
// that local and ssh get, rather than a re-implementation of them that can
// drift. Paths are slash-separated and absolute, as the VFS keeps them.
type vfsFS struct {
	v *vfs
}

var _ transfer.FS = vfsFS{}

func (f vfsFS) Abs(p string) (string, error) { return vfsClean("/", p), nil }
func (f vfsFS) Join(elem ...string) string   { return path.Join(elem...) }
func (f vfsFS) Dir(p string) string          { return path.Dir(p) }
func (f vfsFS) Base(p string) string         { return path.Base(p) }

// Contains reports whether p is root itself or lies under it.
func (f vfsFS) Contains(root, p string) bool {
	return transfer.ContainsPath(root, p, "/")
}

// Paths are resolved the way the host resolves them. The VFS keys each
// entry by its literal path, but a link standing in a parent component is
// a directory to everyone who walks through it: an operation on an entry
// resolves its parent and acts on the entry itself (inParent), and one
// that reaches through the entry resolves the whole path (Resolve). A
// lookup that took the literal path would find nothing under a link, and
// a write would land under a name nothing else could see.

func (f vfsFS) Lstat(p string) (fs.FileInfo, error) {
	q, err := f.inParent(p)
	if err != nil {
		return nil, err
	}

	node, size, ok := f.v.meta(q)
	if !ok {
		return nil, notExist("lstat", p)
	}

	return sizedInfo(path.Base(p), node, size), nil
}

func (f vfsFS) Stat(p string) (fs.FileInfo, error) {
	resolved, err := f.Resolve(p)
	if err != nil {
		return nil, err
	}

	node, size, ok := f.v.meta(resolved)
	if !ok {
		return nil, notExist("stat", p)
	}

	return sizedInfo(path.Base(p), node, size), nil
}

func (f vfsFS) ReadDir(p string) ([]fs.FileInfo, error) {
	dir, err := f.Resolve(p)
	if err != nil {
		return nil, err
	}

	node, _, ok := f.v.meta(dir)
	if !ok {
		return nil, notExist("readdir", p)
	}

	if !node.dir {
		return nil, &fs.PathError{Op: "readdir", Path: p, Err: errors.New("not a directory")}
	}

	names := f.v.children(dir)
	infos := make([]fs.FileInfo, 0, len(names))

	for _, name := range names {
		child, size, ok := f.v.meta(path.Join(dir, name))
		if !ok {
			// Removed between the listing and the lookup: it is no
			// longer part of the tree.
			continue
		}

		infos = append(infos, sizedInfo(name, child, size))
	}

	return infos, nil
}

func (f vfsFS) Mkdir(p string) error {
	q, err := f.inParent(p)
	if err != nil {
		return err
	}

	return f.v.mkdir(q)
}

// MkdirAll creates p and its missing parents, walking through any link to
// a directory it meets, as the host's does.
func (f vfsFS) MkdirAll(p string) error {
	cur := "/"

	for _, name := range splitVFSPath(vfsClean("/", p)) {
		if name == "" {
			continue
		}

		next := path.Join(cur, name)

		node, _, ok := f.v.meta(next)

		switch {
		case !ok:
			if err := f.v.mkdir(next); err != nil {
				// Created by someone else in the meantime is as good.
				if made, _, ok := f.v.meta(next); !ok || !made.dir {
					return err
				}
			}

			cur = next
		case node.link != "":
			resolved, err := f.Resolve(next)
			if err != nil {
				return err
			}

			if target, _, ok := f.v.meta(resolved); !ok || !target.dir {
				return &fs.PathError{Op: "mkdir", Path: next, Err: errors.New("not a directory")}
			}

			cur = resolved
		case node.dir:
			cur = next
		default:
			return &fs.PathError{Op: "mkdir", Path: next, Err: errors.New("not a directory")}
		}
	}

	return nil
}

// Chmod changes what p leads to, following a link as chmod does.
func (f vfsFS) Chmod(p string, mode fs.FileMode) error {
	resolved, err := f.Resolve(p)
	if err != nil {
		return err
	}

	return f.v.chmod(resolved, mode)
}

// SameFile reports false: the fake's filesystem is never the other side of
// one of its own transfers, which always cross to or from the host.
func (f vfsFS) SameFile(_, _ fs.FileInfo) bool { return false }

// Open opens what p leads to. Its content is copied once, here, and the
// lookups before it read metadata alone.
func (f vfsFS) Open(p string) (transfer.ReadFile, error) {
	resolved, err := f.Resolve(p)
	if err != nil {
		return nil, err
	}

	node, ok := f.v.snapshot(resolved)
	if !ok {
		return nil, notExist("open", p)
	}

	if node.dir {
		return nil, &fs.PathError{Op: "open", Path: p, Err: errors.New("is a directory")}
	}

	return &vfsReadFile{r: bytes.NewReader(node.content), info: nodeInfo(path.Base(p), node)}, nil
}

// CreateExclusive creates p, failing if anything is there. The entry exists
// from this moment, so a second exclusive create of the name fails, but its
// content commits only on Close, to the same entry.
func (f vfsFS) CreateExclusive(p string) (transfer.WriteFile, error) {
	q, err := f.inParent(p)
	if err != nil {
		return nil, err
	}

	if err := f.v.createExclusive(q); err != nil {
		return nil, err
	}

	return &vfsWriteFile{v: f.v, path: q}, nil
}

func (f vfsFS) Rename(oldPath, newPath string) error {
	from, err := f.inParent(oldPath)
	if err != nil {
		return err
	}

	to, err := f.inParent(newPath)
	if err != nil {
		return err
	}

	return f.v.rename(from, to)
}

func (f vfsFS) Remove(p string) error {
	q, err := f.inParent(p)
	if err != nil {
		return err
	}

	return f.v.remove(q)
}

func (f vfsFS) Symlink(target, link string) error {
	q, err := f.inParent(link)
	if err != nil {
		return err
	}

	return f.v.symlinkExclusive(target, q)
}

func (f vfsFS) Readlink(p string) (string, error) {
	q, err := f.inParent(p)
	if err != nil {
		return "", err
	}

	node, _, ok := f.v.meta(q)
	if !ok {
		return "", notExist("readlink", p)
	}

	if node.link == "" {
		return "", &fs.PathError{Op: "readlink", Path: p, Err: errors.New("not a symbolic link")}
	}

	return node.link, nil
}

// Resolve canonicalizes p, following symbolic links in every component, as
// filepath.EvalSymlinks does on the host. A path that does not exist is an
// error.
func (f vfsFS) Resolve(p string) (string, error) {
	cur := "/"
	queue := splitVFSPath(vfsClean("/", p))
	hops := 0

	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]

		switch name {
		case "", ".":
			continue
		case "..":
			cur = path.Dir(cur)

			continue
		}

		next := path.Join(cur, name)

		node, _, ok := f.v.meta(next)
		if !ok {
			return "", notExist("resolve", p)
		}

		if node.link == "" {
			if len(queue) > 0 && !node.dir {
				return "", &fs.PathError{Op: "resolve", Path: p, Err: errors.New("not a directory")}
			}

			cur = next

			continue
		}

		hops++
		if hops > maxLinkHops {
			return "", &fs.PathError{Op: "resolve", Path: p, Err: errors.New("too many levels of symbolic links")}
		}

		target := node.link
		if path.IsAbs(target) {
			cur = "/"
		}

		queue = append(splitVFSPath(target), queue...)
	}

	return cur, nil
}

// inParent resolves p's parent and keeps its final entry as written: the
// path an operation on the entry itself, rather than on what it leads to,
// acts on.
func (f vfsFS) inParent(p string) (string, error) {
	p = vfsClean("/", p)
	if p == "/" {
		return p, nil
	}

	dir, err := f.Resolve(path.Dir(p))
	if err != nil {
		return "", err
	}

	return path.Join(dir, path.Base(p)), nil
}

// maxLinkHops bounds a chain of links, as a kernel's own resolution does.
const maxLinkHops = 40

// splitVFSPath breaks a slash-separated path into its components.
func splitVFSPath(p string) []string {
	return strings.Split(strings.Trim(p, "/"), "/")
}

func notExist(op, p string) error {
	return &fs.PathError{Op: op, Path: p, Err: fs.ErrNotExist}
}

// vfsReadFile is an opened virtual file: its content as it was when it was
// opened, and its stat.
//
// It offers Read and nothing more. A bytes.Reader also has WriteTo, which
// the engine would let drive the copy, and it writes the whole file in one
// call: cancellation and progress, which the engine checks between writes,
// would then see a file only once it had already arrived. Read alone keeps
// the engine copying in chunks, as it does from a host file.
type vfsReadFile struct {
	r    *bytes.Reader
	info fs.FileInfo
}

func (f *vfsReadFile) Read(p []byte) (int, error) { return f.r.Read(p) }
func (f *vfsReadFile) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f *vfsReadFile) Close() error               { return nil }

// vfsWriteFile fills an exclusively created virtual file, committing what
// was written when it is closed.
type vfsWriteFile struct {
	v    *vfs
	path string
	buf  bytes.Buffer
}

func (w *vfsWriteFile) Write(p []byte) (int, error) { return w.buf.Write(p) }

func (w *vfsWriteFile) Chmod(mode fs.FileMode) error { return w.v.chmod(w.path, mode) }

// Sync has nothing to flush: the content commits on Close.
func (w *vfsWriteFile) Sync() error { return nil }

func (w *vfsWriteFile) Close() error {
	return w.v.fill(w.path, w.buf.Bytes())
}

// meta returns an entry's type, mode and link target without its content,
// and the content's size: what a lookup needs, without copying a file's
// bytes under the lock to answer it.
func (v *vfs) meta(p string) (vnode, int64, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()

	node, ok := v.nodes[p]
	if !ok {
		return vnode{}, 0, false
	}

	return vnode{mode: node.mode, dir: node.dir, link: node.link}, int64(len(node.content)), true
}

// sizedInfo is nodeInfo for an entry read by meta, whose size is carried
// beside it rather than in its content.
func sizedInfo(name string, node vnode, size int64) fs.FileInfo {
	info, _ := nodeInfo(name, node).(fileInfo)
	info.size = size

	return info
}

// The vfs operations below back vfsFS. Each is one step under the lock, so
// a rename replaces its destination atomically, as the engine relies on.

// mkdir creates one directory, whose parent must exist; anything already
// at p is an error.
func (v *vfs) mkdir(p string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if _, ok := v.nodes[p]; ok {
		return &fs.PathError{Op: "mkdir", Path: p, Err: fs.ErrExist}
	}

	if err := v.parentIsDir("mkdir", p); err != nil {
		return err
	}

	v.nodes[p] = &vnode{dir: true, mode: defaultDirMode}

	return nil
}

// chmod sets an entry's permission bits.
func (v *vfs) chmod(p string, mode fs.FileMode) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	node, ok := v.nodes[p]
	if !ok {
		return notExist("chmod", p)
	}

	node.mode = mode.Perm()

	return nil
}

// createExclusive creates an empty file readable and writable only by its
// owner, failing if anything is at p.
func (v *vfs) createExclusive(p string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if _, ok := v.nodes[p]; ok {
		return &fs.PathError{Op: "open", Path: p, Err: fs.ErrExist}
	}

	if err := v.parentIsDir("open", p); err != nil {
		return err
	}

	v.nodes[p] = &vnode{mode: 0o600}

	return nil
}

// fill commits content to a file created by createExclusive.
func (v *vfs) fill(p string, content []byte) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	node, ok := v.nodes[p]
	if !ok || node.dir || node.link != "" {
		return &fs.PathError{Op: "close", Path: p, Err: errors.New("the file being written is gone")}
	}

	node.content = append([]byte(nil), content...)

	return nil
}

// rename moves the entry at oldPath, and everything beneath it, over
// newPath in one step, replacing a file or link there. A directory at
// newPath is not replaced.
func (v *vfs) rename(oldPath, newPath string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	node, ok := v.nodes[oldPath]
	if !ok {
		return &fs.PathError{Op: "rename", Path: oldPath, Err: fs.ErrNotExist}
	}

	if existing, ok := v.nodes[newPath]; ok && existing.dir {
		return &fs.PathError{Op: "rename", Path: newPath, Err: errors.New("is a directory")}
	}

	if err := v.parentIsDir("rename", newPath); err != nil {
		return err
	}

	if node.dir {
		prefix := dirPrefix(oldPath)
		moved := make(map[string]*vnode)

		for candidate, child := range v.nodes {
			if rest, ok := strings.CutPrefix(candidate, prefix); ok {
				moved[dirPrefix(newPath)+rest] = child

				delete(v.nodes, candidate)
			}
		}

		maps.Copy(v.nodes, moved)
	}

	delete(v.nodes, oldPath)
	v.nodes[newPath] = node

	return nil
}

// remove deletes a file, a link or an empty directory.
func (v *vfs) remove(p string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	node, ok := v.nodes[p]
	if !ok {
		return notExist("remove", p)
	}

	if node.dir {
		prefix := dirPrefix(p)

		for candidate := range v.nodes {
			if strings.HasPrefix(candidate, prefix) {
				return &fs.PathError{Op: "remove", Path: p, Err: errors.New("directory not empty")}
			}
		}
	}

	delete(v.nodes, p)

	return nil
}

// symlinkExclusive creates a link at p, failing if anything is there.
func (v *vfs) symlinkExclusive(target, p string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if _, ok := v.nodes[p]; ok {
		return &fs.PathError{Op: "symlink", Path: p, Err: fs.ErrExist}
	}

	if err := v.parentIsDir("symlink", p); err != nil {
		return err
	}

	v.nodes[p] = &vnode{link: target, mode: defaultFileMode}

	return nil
}

// parentIsDir reports an error unless p's parent is a directory. The
// caller holds the lock.
func (v *vfs) parentIsDir(op, p string) error {
	parent, ok := v.nodes[path.Dir(p)]
	if !ok || !parent.dir {
		return &fs.PathError{Op: op, Path: p, Err: fmt.Errorf("parent %q is not a directory", path.Dir(p))}
	}

	return nil
}
