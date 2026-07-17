package fake

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"time"
)

// FS returns a read-only [io/fs.FS] view of the fake target's filesystem,
// for asserting on target state with standard tooling (fs.ReadFile,
// fs.WalkDir, fstest.TestFS). Paths are unrooted io/fs paths: the
// target's /tmp/app.txt is "tmp/app.txt", and "." is the root.
//
// The view is live — it reflects mutations as commands and transfers make
// them — and implements [fs.ReadLinkFS], so symlinks are inspectable.
func (e *Environment) FS() fs.FS {
	return &fsView{v: e.fs}
}

type fsView struct {
	v *vfs
}

var (
	_ fs.FS         = (*fsView)(nil)
	_ fs.ReadLinkFS = (*fsView)(nil)
)

// maxLinkDepth bounds symlink resolution in Open, guarding cycles.
const maxLinkDepth = 8

func toVFSPath(name string) (string, bool) {
	if !fs.ValidPath(name) {
		return "", false
	}

	if name == "." {
		return "/", true
	}

	return "/" + name, true
}

// Open opens the named file, following symlinks.
func (f *fsView) Open(name string) (fs.File, error) {
	vpath, ok := toVFSPath(name)
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}

	node, resolved, err := f.resolve(vpath, maxLinkDepth)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}

	if node.dir {
		return &dirHandle{view: f, vpath: resolved, name: name}, nil
	}

	return &fileHandle{
		reader: bytes.NewReader(node.content),
		info:   nodeInfo(path.Base(resolved), node),
	}, nil
}

// Lstat describes the named file without following a final symlink.
func (f *fsView) Lstat(name string) (fs.FileInfo, error) {
	vpath, ok := toVFSPath(name)
	if !ok {
		return nil, &fs.PathError{Op: "lstat", Path: name, Err: fs.ErrInvalid}
	}

	node, exists := f.v.snapshot(vpath)
	if !exists {
		return nil, &fs.PathError{Op: "lstat", Path: name, Err: fs.ErrNotExist}
	}

	return nodeInfo(path.Base(vpath), node), nil
}

// ReadLink returns the destination of the named symbolic link.
func (f *fsView) ReadLink(name string) (string, error) {
	vpath, ok := toVFSPath(name)
	if !ok {
		return "", &fs.PathError{Op: "readlink", Path: name, Err: fs.ErrInvalid}
	}

	node, exists := f.v.snapshot(vpath)
	if !exists {
		return "", &fs.PathError{Op: "readlink", Path: name, Err: fs.ErrNotExist}
	}

	if node.link == "" {
		return "", &fs.PathError{Op: "readlink", Path: name, Err: fs.ErrInvalid}
	}

	return node.link, nil
}

// resolve walks symlinks to the underlying node.
func (f *fsView) resolve(vpath string, depth int) (vnode, string, error) {
	node, exists := f.v.snapshot(vpath)
	if !exists {
		return vnode{}, "", fs.ErrNotExist
	}

	if node.link == "" {
		return node, vpath, nil
	}

	if depth == 0 {
		return vnode{}, "", errors.New("too many levels of symbolic links")
	}

	target := node.link
	if !path.IsAbs(target) {
		target = path.Join(path.Dir(vpath), target)
	}

	return f.resolve(path.Clean(target), depth-1)
}

// nodeInfo materializes an fs.FileInfo for a node snapshot.
func nodeInfo(name string, node vnode) fs.FileInfo {
	mode := node.mode.Perm()

	switch {
	case node.dir:
		mode |= fs.ModeDir
	case node.link != "":
		mode |= fs.ModeSymlink
	}

	return fileInfo{
		name: name,
		size: int64(len(node.content)),
		mode: mode,
	}
}

type fileInfo struct {
	name string
	size int64
	mode fs.FileMode
}

func (i fileInfo) Name() string       { return i.name }
func (i fileInfo) Size() int64        { return i.size }
func (i fileInfo) Mode() fs.FileMode  { return i.mode }
func (i fileInfo) ModTime() time.Time { return time.Time{} }
func (i fileInfo) IsDir() bool        { return i.mode.IsDir() }
func (i fileInfo) Sys() any           { return nil }

// fileHandle is an open regular file.
type fileHandle struct {
	reader *bytes.Reader
	info   fs.FileInfo
}

func (h *fileHandle) Stat() (fs.FileInfo, error) { return h.info, nil }
func (h *fileHandle) Read(p []byte) (int, error) { return h.reader.Read(p) }
func (h *fileHandle) Close() error               { return nil }

// dirHandle is an open directory supporting paged ReadDir.
type dirHandle struct {
	view   *fsView
	vpath  string
	name   string
	cursor int
}

func (h *dirHandle) Stat() (fs.FileInfo, error) {
	node, exists := h.view.v.snapshot(h.vpath)
	if !exists {
		return nil, &fs.PathError{Op: "stat", Path: h.name, Err: fs.ErrNotExist}
	}

	return nodeInfo(path.Base(h.vpath), node), nil
}

func (h *dirHandle) Read(_ []byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: h.name, Err: errors.New("is a directory")}
}

func (h *dirHandle) Close() error { return nil }

// ReadDir lists the directory's entries, honoring fs.ReadDirFile paging.
func (h *dirHandle) ReadDir(n int) ([]fs.DirEntry, error) {
	names := h.view.v.children(h.vpath)

	if h.cursor >= len(names) {
		if n <= 0 {
			return nil, nil
		}

		return nil, io.EOF
	}

	remaining := names[h.cursor:]
	if n > 0 && len(remaining) > n {
		remaining = remaining[:n]
	}

	entries := make([]fs.DirEntry, 0, len(remaining))

	for _, name := range remaining {
		node, exists := h.view.v.snapshot(path.Join(h.vpath, name))
		if !exists {
			continue
		}

		entries = append(entries, dirEntry{info: nodeInfo(name, node)})
	}

	h.cursor += len(remaining)

	return entries, nil
}

// dirEntry adapts a fileInfo to fs.DirEntry.
type dirEntry struct {
	info fs.FileInfo
}

func (d dirEntry) Name() string               { return d.info.Name() }
func (d dirEntry) IsDir() bool                { return d.info.IsDir() }
func (d dirEntry) Type() fs.FileMode          { return d.info.Mode().Type() }
func (d dirEntry) Info() (fs.FileInfo, error) { return d.info, nil }

// String implements fmt.Stringer for diagnostics.
func (d dirEntry) String() string {
	return fmt.Sprintf("%s %s", d.info.Mode(), d.info.Name())
}
