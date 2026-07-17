package fake

import (
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
)

// vnode is one entry in the simulated target filesystem.
type vnode struct {
	mode    fs.FileMode // permission bits
	content []byte      // regular-file content
	dir     bool
	link    string // symlink target when non-empty
}

// vfs is the fake target's filesystem: absolute, slash-separated, cleaned
// paths mapping to nodes. It is safe for concurrent use.
type vfs struct {
	mu    sync.Mutex
	nodes map[string]*vnode
}

const (
	defaultDirMode  = fs.FileMode(0o755)
	defaultFileMode = fs.FileMode(0o644)
)

func newVFS() *vfs {
	return &vfs{nodes: map[string]*vnode{
		"/":    {dir: true, mode: defaultDirMode},
		"/tmp": {dir: true, mode: defaultDirMode},
	}}
}

// clean normalizes p to an absolute cleaned path, resolving relative paths
// against dir.
func vfsClean(dir, p string) string {
	if !path.IsAbs(p) {
		p = path.Join(dir, p)
	}

	return path.Clean(p)
}

func (v *vfs) get(p string) (*vnode, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()

	node, ok := v.nodes[p]

	return node, ok
}

func (v *vfs) isDir(p string) bool {
	node, ok := v.get(p)

	return ok && node.dir
}

func (v *vfs) mkdirAll(p string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	segments := strings.Split(path.Clean(p), "/")
	current := "/"

	for _, segment := range segments {
		if segment == "" {
			continue
		}

		current = path.Join(current, segment)

		node, ok := v.nodes[current]
		if ok {
			if !node.dir {
				return fmt.Errorf("fake: %q is not a directory", current)
			}

			continue
		}

		v.nodes[current] = &vnode{dir: true, mode: defaultDirMode}
	}

	return nil
}

// writeFile commits a regular file. The parent directory must exist.
func (v *vfs) writeFile(p string, content []byte, mode fs.FileMode) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	parent, ok := v.nodes[path.Dir(p)]
	if !ok || !parent.dir {
		return fmt.Errorf("fake: parent of %q does not exist", p)
	}

	if existing, exists := v.nodes[p]; exists && existing.dir {
		return fmt.Errorf("fake: %q is a directory", p)
	}

	v.nodes[p] = &vnode{content: append([]byte(nil), content...), mode: mode}

	return nil
}

// symlink commits a symbolic link, replacing any existing node.
func (v *vfs) symlink(target, p string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	parent, ok := v.nodes[path.Dir(p)]
	if !ok || !parent.dir {
		return fmt.Errorf("fake: parent of %q does not exist", p)
	}

	v.nodes[p] = &vnode{link: target, mode: defaultFileMode}

	return nil
}

// setDirMode records a directory's mode.
func (v *vfs) setDirMode(p string, mode fs.FileMode) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if node, ok := v.nodes[p]; ok && node.dir {
		node.mode = mode
	}
}

// touch creates an empty file if absent.
func (v *vfs) touch(p string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if _, ok := v.nodes[p]; ok {
		return nil
	}

	parent, ok := v.nodes[path.Dir(p)]
	if !ok || !parent.dir {
		return fmt.Errorf("fake: parent of %q does not exist", p)
	}

	v.nodes[p] = &vnode{mode: defaultFileMode}

	return nil
}

// dirPrefix renders the child-matching prefix for a directory path,
// handling the root (where a naive p+"/" would produce "//" and match
// nothing).
func dirPrefix(p string) string {
	p = path.Clean(p)
	if p == "/" {
		return "/"
	}

	return p + "/"
}

// removeAll deletes p and everything beneath it; absent paths are fine.
func (v *vfs) removeAll(p string) {
	v.mu.Lock()
	defer v.mu.Unlock()

	cleaned := path.Clean(p)
	prefix := dirPrefix(cleaned)

	for candidate := range v.nodes {
		if candidate == cleaned || strings.HasPrefix(candidate, prefix) {
			delete(v.nodes, candidate)
		}
	}
}

// children lists the immediate child names of directory p, sorted.
func (v *vfs) children(p string) []string {
	v.mu.Lock()
	defer v.mu.Unlock()

	prefix := dirPrefix(p)

	var names []string

	for candidate := range v.nodes {
		if candidate == "/" || !strings.HasPrefix(candidate, prefix) {
			continue
		}

		rest := strings.TrimPrefix(candidate, prefix)
		if !strings.Contains(rest, "/") {
			names = append(names, rest)
		}
	}

	sort.Strings(names)

	return names
}

// snapshot returns a copy of a node's data for race-free reads.
func (v *vfs) snapshot(p string) (vnode, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()

	node, ok := v.nodes[p]
	if !ok {
		return vnode{}, false
	}

	copied := *node
	copied.content = append([]byte(nil), node.content...)

	return copied, true
}
