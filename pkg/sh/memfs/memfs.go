package memfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/richardartoul/swarmd/pkg/sh/interp"
)

const maxSymlinkDepth = 40

var (
	errBadFD             = errors.New("bad file descriptor")
	errFIFOBusy          = errors.New("fifo already open")
	errIsDirectory       = errors.New("is a directory")
	errNotDirectory      = errors.New("not a directory")
	errDirectoryNotEmpty = errors.New("directory not empty")
	errTooManySymlinks   = errors.New("too many symlink evaluations")
)

type nodeKind uint8

const (
	nodeRegular nodeKind = iota
	nodeDirectory
	nodeSymlink
	nodeFIFO
)

type node struct {
	kind     nodeKind
	mode     fs.FileMode
	uid      uint32
	gid      uint32
	atime    time.Time
	mtime    time.Time
	ino      uint64
	nlink    uint64
	data     []byte
	target   string
	children map[string]*node
	fifo     *fifoState
}

type fifoState struct {
	mu           sync.Mutex
	cond         *sync.Cond
	data         []byte
	readOpen     bool
	writeOpen    bool
	readerClosed bool
	writerClosed bool
}

// FS is an in-memory filesystem that satisfies the shell sandbox contracts.
type FS struct {
	root string
	uid  uint32
	gid  uint32

	mu       sync.RWMutex
	nextIno  uint64
	rootNode *node
}

var _ interp.RunnerFileSystem = (*FS)(nil)
var _ interp.MutableFileSystem = (*FS)(nil)
var _ interp.WorkingDirFileSystem = (*FS)(nil)
var _ interp.ReadlinkFileSystem = (*FS)(nil)
var _ interp.RenameFileSystem = (*FS)(nil)
var _ interp.ChmodFileSystem = (*FS)(nil)
var _ interp.TempDirFileSystem = (*FS)(nil)
var _ interp.ResolvePathFileSystem = (*FS)(nil)
var _ interp.AccessFileSystem = (*FS)(nil)
var _ interp.SameFileFileSystem = (*FS)(nil)
var _ interp.OwnerIDFileSystem = (*FS)(nil)
var _ interp.EvalSymlinksFileSystem = (*FS)(nil)
var _ interp.MkfifoFileSystem = (*FS)(nil)
var _ interp.HostPathFileSystem = (*FS)(nil)

// New constructs a new in-memory filesystem rooted at the provided logical
// absolute path. An empty root defaults to "/".
func New(root string) (*FS, error) {
	root, err := cleanRoot(root)
	if err != nil {
		return nil, err
	}
	uid, gid := currentOwnerIDs()
	fsys := &FS{
		root:    root,
		uid:     uid,
		gid:     gid,
		nextIno: 1,
	}
	now := time.Now()
	fsys.rootNode = fsys.newNodeLocked(nodeDirectory, fs.ModeDir|0o755, now)
	return fsys, nil
}

func (f *FS) Getwd() (string, error) {
	return f.root, nil
}

func (f *FS) TempDir() string {
	return filepath.Join(f.root, ".tmp")
}

func (f *FS) ResolvePath(dir, path string) (string, error) {
	if dir == "" {
		dir = f.root
	}
	switch {
	case path == "":
		path = dir
	case !isRootedPath(path):
		path = filepath.Join(dir, path)
	}
	path = filepath.Clean(path)
	if !isWithinRoot(f.root, path) {
		return "", pathError("resolve", path, fs.ErrPermission)
	}
	return path, nil
}

func (f *FS) MapPath(path string) (string, error) {
	if interp.IsDevNullPath(path) {
		return path, nil
	}
	if !isRootedPath(path) {
		return path, nil
	}
	return f.ResolvePath(f.root, path)
}

func (f *FS) HostPath(path string) (string, error) {
	return "", interp.ErrNoHostPath
}

func (f *FS) Open(name string) (fs.File, error) {
	if interp.IsDevNullPath(name) {
		return interp.OSFileSystem{}.Open(name)
	}
	path, err := f.ResolvePath(f.root, name)
	if err != nil {
		return nil, err
	}
	f.mu.RLock()
	node, resolved, err := f.lookupPathLocked(path, true, 0)
	f.mu.RUnlock()
	if err != nil {
		return nil, pathError("open", path, err)
	}
	switch node.kind {
	case nodeDirectory:
		return &dirFile{fs: f, path: resolved, node: node}, nil
	case nodeFIFO:
		handle, err := f.openFIFOLocked(resolved, node, os.O_RDONLY)
		if err != nil {
			return nil, err
		}
		if file, ok := handle.(fs.File); ok {
			return file, nil
		}
		return nil, pathError("open", resolved, errBadFD)
	default:
		return &regularFile{
			fs:       f,
			path:     resolved,
			node:     node,
			readable: true,
		}, nil
	}
}

func (f *FS) ReadDir(name string) ([]fs.DirEntry, error) {
	path, err := f.ResolvePath(f.root, name)
	if err != nil {
		return nil, err
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	node, resolved, err := f.lookupPathLocked(path, true, 0)
	if err != nil {
		return nil, pathError("readdir", path, err)
	}
	if node.kind != nodeDirectory {
		return nil, pathError("readdir", resolved, errNotDirectory)
	}
	return dirEntriesForNode(node), nil
}

func (f *FS) Stat(name string) (fs.FileInfo, error) {
	if interp.IsDevNullPath(name) {
		return interp.OSFileSystem{}.Stat(name)
	}
	path, err := f.ResolvePath(f.root, name)
	if err != nil {
		return nil, err
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	node, resolved, err := f.lookupPathLocked(path, true, 0)
	if err != nil {
		return nil, pathError("stat", path, err)
	}
	return snapshotInfo(filepath.Base(resolved), node), nil
}

func (f *FS) Lstat(name string) (fs.FileInfo, error) {
	if interp.IsDevNullPath(name) {
		return interp.OSFileSystem{}.Lstat(name)
	}
	path, err := f.ResolvePath(f.root, name)
	if err != nil {
		return nil, err
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	node, resolved, err := f.lookupPathLocked(path, false, 0)
	if err != nil {
		return nil, pathError("lstat", path, err)
	}
	return snapshotInfo(filepath.Base(resolved), node), nil
}

func (f *FS) OpenFile(name string, flag int, perm os.FileMode) (io.ReadWriteCloser, error) {
	if interp.IsDevNullPath(name) {
		return interp.OSFileSystem{}.OpenFile(name, flag, perm)
	}
	path, err := f.ResolvePath(f.root, name)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	node, resolved, err := f.lookupPathLocked(path, true, 0)
	switch {
	case err == nil:
		if flag&os.O_CREATE != 0 && flag&os.O_EXCL != 0 {
			return nil, pathError("open", resolved, fs.ErrExist)
		}
		switch node.kind {
		case nodeDirectory:
			return nil, pathError("open", resolved, errIsDirectory)
		case nodeFIFO:
			return f.openFIFOLocked(resolved, node, flag)
		default:
			if flag&(os.O_WRONLY|os.O_RDWR) != 0 && flag&os.O_TRUNC != 0 && flag&os.O_APPEND == 0 {
				node.data = nil
				node.mtime = time.Now()
			}
			return &regularFile{
				fs:       f,
				path:     resolved,
				node:     node,
				readable: flag&os.O_WRONLY == 0,
				writable: flag&(os.O_WRONLY|os.O_RDWR) != 0,
				append:   flag&os.O_APPEND != 0,
				offset:   fileOffset(node, flag),
			}, nil
		}
	case !errors.Is(err, fs.ErrNotExist):
		return nil, pathError("open", path, err)
	case flag&os.O_CREATE == 0:
		return nil, pathError("open", path, err)
	}

	createPath, err := f.resolveCreatePathLocked(path, 0)
	if err != nil {
		return nil, pathError("open", path, err)
	}
	parent, _, base, err := f.lookupParentLocked(createPath)
	if err != nil {
		return nil, pathError("open", createPath, err)
	}
	if _, exists := parent.children[base]; exists {
		return nil, pathError("open", createPath, fs.ErrExist)
	}
	now := time.Now()
	node = f.newNodeLocked(nodeRegular, perm&0o777, now)
	parent.children[base] = node
	return &regularFile{
		fs:       f,
		path:     createPath,
		node:     node,
		readable: flag&os.O_WRONLY == 0,
		writable: true,
		append:   flag&os.O_APPEND != 0,
		offset:   fileOffset(node, flag),
	}, nil
}

func (f *FS) Remove(name string) error {
	path, err := f.ResolvePath(f.root, name)
	if err != nil {
		return err
	}
	if path == f.root {
		return pathError("remove", path, fs.ErrPermission)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	parent, _, base, err := f.lookupParentLocked(path)
	if err != nil {
		return pathError("remove", path, err)
	}
	node, ok := parent.children[base]
	if !ok {
		return pathError("remove", path, fs.ErrNotExist)
	}
	if node.kind == nodeDirectory && len(node.children) > 0 {
		return pathError("remove", path, errDirectoryNotEmpty)
	}
	delete(parent.children, base)
	f.discardNodeLocked(node, false)
	return nil
}

func (f *FS) Readlink(name string) (string, error) {
	path, err := f.ResolvePath(f.root, name)
	if err != nil {
		return "", err
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	node, _, err := f.lookupPathLocked(path, false, 0)
	if err != nil {
		return "", pathError("readlink", path, err)
	}
	if node.kind != nodeSymlink {
		return "", pathError("readlink", path, fs.ErrInvalid)
	}
	return node.target, nil
}

func (f *FS) Rename(oldpath, newpath string) error {
	oldpath, err := f.ResolvePath(f.root, oldpath)
	if err != nil {
		return err
	}
	newpath, err = f.ResolvePath(f.root, newpath)
	if err != nil {
		return err
	}
	if oldpath == f.root {
		return pathError("rename", oldpath, fs.ErrPermission)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	oldParent, _, oldBase, err := f.lookupParentLocked(oldpath)
	if err != nil {
		return pathError("rename", oldpath, err)
	}
	node, ok := oldParent.children[oldBase]
	if !ok {
		return pathError("rename", oldpath, fs.ErrNotExist)
	}
	newParent, _, newBase, err := f.lookupParentLocked(newpath)
	if err != nil {
		return pathError("rename", newpath, err)
	}
	if existing, ok := newParent.children[newBase]; ok {
		if existing.kind == nodeDirectory && len(existing.children) > 0 {
			return pathError("rename", newpath, errDirectoryNotEmpty)
		}
		delete(newParent.children, newBase)
		f.discardNodeLocked(existing, false)
	}
	delete(oldParent.children, oldBase)
	newParent.children[newBase] = node
	return nil
}

func (f *FS) Chmod(name string, mode os.FileMode) error {
	path, err := f.ResolvePath(f.root, name)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	node, _, err := f.lookupPathLocked(path, true, 0)
	if err != nil {
		return pathError("chmod", path, err)
	}
	node.mode = (node.mode & fs.ModeType) | (mode & 0o777)
	node.mtime = time.Now()
	return nil
}

func (f *FS) Mkdir(name string, perm os.FileMode) error {
	path, err := f.ResolvePath(f.root, name)
	if err != nil {
		return err
	}
	if path == f.root {
		return pathError("mkdir", path, fs.ErrExist)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	parent, _, base, err := f.lookupParentLocked(path)
	if err != nil {
		return pathError("mkdir", path, err)
	}
	if _, exists := parent.children[base]; exists {
		return pathError("mkdir", path, fs.ErrExist)
	}
	parent.children[base] = f.newNodeLocked(nodeDirectory, fs.ModeDir|(perm&0o777), time.Now())
	return nil
}

func (f *FS) MkdirAll(name string, perm os.FileMode) error {
	path, err := f.ResolvePath(f.root, name)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if path == f.root {
		return nil
	}
	parts, err := splitWithinRoot(f.root, path)
	if err != nil {
		return err
	}
	current := f.rootNode
	currentPath := f.root
	for _, part := range parts {
		child, ok := current.children[part]
		if !ok {
			child = f.newNodeLocked(nodeDirectory, fs.ModeDir|(perm&0o777), time.Now())
			current.children[part] = child
			current = child
			currentPath = filepath.Join(currentPath, part)
			continue
		}
		if child.kind == nodeSymlink {
			targetPath, err := f.ResolvePath(currentPath, child.target)
			if err != nil {
				return err
			}
			targetNode, resolvedTarget, err := f.lookupPathLocked(targetPath, true, 0)
			if err != nil {
				return err
			}
			if targetNode.kind != nodeDirectory {
				return pathError("mkdir", resolvedTarget, errNotDirectory)
			}
			current = targetNode
			currentPath = resolvedTarget
			continue
		}
		if child.kind != nodeDirectory {
			return pathError("mkdir", filepath.Join(currentPath, part), errNotDirectory)
		}
		current = child
		currentPath = filepath.Join(currentPath, part)
	}
	return nil
}

func (f *FS) RemoveAll(name string) error {
	path, err := f.ResolvePath(f.root, name)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if path == f.root {
		for name, child := range f.rootNode.children {
			delete(f.rootNode.children, name)
			f.discardNodeLocked(child, true)
		}
		return nil
	}
	parent, _, base, err := f.lookupParentLocked(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return pathError("remove", path, err)
	}
	child, ok := parent.children[base]
	if !ok {
		return nil
	}
	delete(parent.children, base)
	f.discardNodeLocked(child, true)
	return nil
}

func (f *FS) WriteFile(name string, data []byte, perm os.FileMode) error {
	file, err := f.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	return nil
}

func (f *FS) Link(oldname, newname string) error {
	oldname, err := f.ResolvePath(f.root, oldname)
	if err != nil {
		return err
	}
	newname, err = f.ResolvePath(f.root, newname)
	if err != nil {
		return err
	}
	if newname == f.root {
		return pathError("link", newname, fs.ErrPermission)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	oldNode, _, err := f.lookupPathLocked(oldname, true, 0)
	if err != nil {
		return pathError("link", oldname, err)
	}
	if oldNode.kind == nodeDirectory {
		return pathError("link", oldname, fs.ErrPermission)
	}
	parent, _, base, err := f.lookupParentLocked(newname)
	if err != nil {
		return pathError("link", newname, err)
	}
	if _, exists := parent.children[base]; exists {
		return pathError("link", newname, fs.ErrExist)
	}
	parent.children[base] = oldNode
	oldNode.nlink++
	return nil
}

func (f *FS) Symlink(oldname, newname string) error {
	newname, err := f.ResolvePath(f.root, newname)
	if err != nil {
		return err
	}
	if newname == f.root {
		return pathError("symlink", newname, fs.ErrPermission)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	parent, _, base, err := f.lookupParentLocked(newname)
	if err != nil {
		return pathError("symlink", newname, err)
	}
	if _, exists := parent.children[base]; exists {
		return pathError("symlink", newname, fs.ErrExist)
	}
	now := time.Now()
	node := f.newNodeLocked(nodeSymlink, fs.ModeSymlink|0o777, now)
	node.target = oldname
	parent.children[base] = node
	return nil
}

func (f *FS) Chtimes(name string, atime, mtime time.Time) error {
	path, err := f.ResolvePath(f.root, name)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	node, _, err := f.lookupPathLocked(path, true, 0)
	if err != nil {
		return pathError("chtimes", path, err)
	}
	node.atime = atime
	node.mtime = mtime
	return nil
}

func (f *FS) Access(name string, mode uint32) error {
	if interp.IsDevNullPath(name) {
		if mode&interp.AccessExecute != 0 {
			return fmt.Errorf("file is not executable")
		}
		return nil
	}
	path, err := f.ResolvePath(f.root, name)
	if err != nil {
		return err
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	node, _, err := f.lookupPathLocked(path, true, 0)
	if err != nil {
		return err
	}
	perms := effectivePermBits(f.uid, f.gid, node)
	switch {
	case mode&interp.AccessRead != 0 && perms&0o4 == 0:
		return fmt.Errorf("file is not readable")
	case mode&interp.AccessWrite != 0 && perms&0o2 == 0:
		return fmt.Errorf("file is not writable")
	case mode&interp.AccessExecute != 0 && perms&0o1 == 0:
		return fmt.Errorf("file is not executable")
	default:
		return nil
	}
}

func (f *FS) SameFile(firstPath string, firstInfo fs.FileInfo, secondPath string, secondInfo fs.FileInfo) bool {
	firstStat, ok1 := firstInfo.(*statInfo)
	secondStat, ok2 := secondInfo.(*statInfo)
	return ok1 && ok2 && firstStat.ino == secondStat.ino
}

func (f *FS) OwnerIDs(info fs.FileInfo) (uid, gid uint32, ok bool) {
	stat, ok := info.(*statInfo)
	if !ok {
		return 0, 0, false
	}
	return stat.uid, stat.gid, true
}

func (f *FS) EvalSymlinks(path string) (string, error) {
	if interp.IsDevNullPath(path) {
		return path, nil
	}
	path, err := f.ResolvePath(f.root, path)
	if err != nil {
		return "", err
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	_, resolved, err := f.lookupPathLocked(path, true, 0)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

func (f *FS) Mkfifo(name string, mode uint32) error {
	path, err := f.ResolvePath(f.root, name)
	if err != nil {
		return err
	}
	if path == f.root {
		return pathError("mkfifo", path, fs.ErrExist)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	parent, _, base, err := f.lookupParentLocked(path)
	if err != nil {
		return pathError("mkfifo", path, err)
	}
	if _, exists := parent.children[base]; exists {
		return pathError("mkfifo", path, fs.ErrExist)
	}
	now := time.Now()
	node := f.newNodeLocked(nodeFIFO, fs.ModeNamedPipe|fs.FileMode(mode&0o777), now)
	node.fifo = newFIFOState()
	parent.children[base] = node
	return nil
}

func (f *FS) newNodeLocked(kind nodeKind, mode fs.FileMode, now time.Time) *node {
	entry := &node{
		kind:  kind,
		mode:  mode,
		uid:   f.uid,
		gid:   f.gid,
		atime: now,
		mtime: now,
		ino:   f.nextIno,
		nlink: 1,
	}
	f.nextIno++
	if kind == nodeDirectory {
		entry.children = make(map[string]*node)
	}
	return entry
}

func (f *FS) lookupParentLocked(path string) (*node, string, string, error) {
	if path == f.root {
		return nil, "", "", fs.ErrPermission
	}
	parentPath := filepath.Dir(path)
	base := filepath.Base(path)
	parent, resolvedParent, err := f.lookupPathLocked(parentPath, true, 0)
	if err != nil {
		return nil, "", "", err
	}
	if parent.kind != nodeDirectory {
		return nil, "", "", errNotDirectory
	}
	return parent, resolvedParent, base, nil
}

func (f *FS) lookupPathLocked(path string, followFinal bool, depth int) (*node, string, error) {
	if depth > maxSymlinkDepth {
		return nil, "", errTooManySymlinks
	}
	if path == f.root {
		return f.rootNode, f.root, nil
	}
	parts, err := splitWithinRoot(f.root, path)
	if err != nil {
		return nil, "", err
	}
	current := f.rootNode
	currentPath := f.root
	for i, part := range parts {
		if current.kind != nodeDirectory {
			return nil, "", errNotDirectory
		}
		child, ok := current.children[part]
		if !ok {
			return nil, "", fs.ErrNotExist
		}
		last := i == len(parts)-1
		if child.kind == nodeSymlink && (!last || followFinal) {
			targetPath, err := f.ResolvePath(currentPath, child.target)
			if err != nil {
				return nil, "", err
			}
			if !last {
				remaining := filepath.Join(parts[i+1:]...)
				targetPath, err = f.ResolvePath(targetPath, remaining)
				if err != nil {
					return nil, "", err
				}
			}
			return f.lookupPathLocked(targetPath, followFinal, depth+1)
		}
		current = child
		currentPath = filepath.Join(currentPath, part)
	}
	return current, currentPath, nil
}

func (f *FS) resolveCreatePathLocked(path string, depth int) (string, error) {
	if depth > maxSymlinkDepth {
		return "", errTooManySymlinks
	}
	parent, parentPath, base, err := f.lookupParentLocked(path)
	if err != nil {
		return "", err
	}
	child, ok := parent.children[base]
	if !ok || child.kind != nodeSymlink {
		return path, nil
	}
	targetPath, err := f.ResolvePath(parentPath, child.target)
	if err != nil {
		return "", err
	}
	return f.resolveCreatePathLocked(targetPath, depth+1)
}

func (f *FS) discardNodeLocked(node *node, recursive bool) {
	switch node.kind {
	case nodeDirectory:
		if recursive {
			for name, child := range node.children {
				delete(node.children, name)
				f.discardNodeLocked(child, true)
			}
		}
	default:
		if node.nlink > 0 {
			node.nlink--
		}
	}
}

func (f *FS) openFIFOLocked(path string, node *node, flag int) (io.ReadWriteCloser, error) {
	if node.fifo == nil {
		node.fifo = newFIFOState()
	}
	readable := flag&os.O_WRONLY == 0
	writable := flag&(os.O_WRONLY|os.O_RDWR) != 0
	if readable && writable {
		return nil, pathError("open", path, fs.ErrInvalid)
	}
	node.fifo.mu.Lock()
	defer node.fifo.mu.Unlock()
	if !node.fifo.readOpen && !node.fifo.writeOpen {
		node.fifo.data = nil
		node.fifo.readerClosed = false
		node.fifo.writerClosed = false
	}
	if readable {
		if node.fifo.readOpen {
			return nil, pathError("open", path, errFIFOBusy)
		}
		node.fifo.readOpen = true
		return &fifoReadFile{fs: f, path: path, node: node, state: node.fifo}, nil
	}
	if writable {
		if node.fifo.writeOpen {
			return nil, pathError("open", path, errFIFOBusy)
		}
		node.fifo.writeOpen = true
		return &fifoWriteFile{fs: f, path: path, node: node, state: node.fifo}, nil
	}
	return nil, pathError("open", path, fs.ErrInvalid)
}

func fileOffset(node *node, flag int) int64 {
	if flag&os.O_APPEND != 0 {
		return int64(len(node.data))
	}
	return 0
}

func dirEntriesForNode(node *node) []fs.DirEntry {
	names := make([]string, 0, len(node.children))
	for name := range node.children {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]fs.DirEntry, 0, len(names))
	for _, name := range names {
		entries = append(entries, dirEntry{info: snapshotInfo(name, node.children[name])})
	}
	return entries
}

func splitWithinRoot(root, path string) ([]string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil, err
	}
	if rel == "." {
		return nil, nil
	}
	return strings.Split(rel, string(os.PathSeparator)), nil
}

func snapshotInfo(name string, node *node) *statInfo {
	return &statInfo{
		name:    name,
		size:    nodeSize(node),
		mode:    node.mode,
		modTime: node.mtime,
		atime:   node.atime,
		uid:     node.uid,
		gid:     node.gid,
		ino:     node.ino,
	}
}

func nodeSize(node *node) int64 {
	switch node.kind {
	case nodeRegular:
		return int64(len(node.data))
	case nodeSymlink:
		return int64(len(node.target))
	default:
		return 0
	}
}

func effectivePermBits(uid, gid uint32, node *node) fs.FileMode {
	if uid == 0 {
		if node.mode&0o111 != 0 {
			return 0o7
		}
		return 0o6
	}
	switch {
	case uid == node.uid:
		return (node.mode >> 6) & 0o7
	case gid == node.gid:
		return (node.mode >> 3) & 0o7
	default:
		return node.mode & 0o7
	}
}

func currentOwnerIDs() (uint32, uint32) {
	current, err := user.Current()
	if err != nil {
		return 0, 0
	}
	uid, _ := strconv.Atoi(current.Uid)
	gid, _ := strconv.Atoi(current.Gid)
	return uint32(uid), uint32(gid)
}

func cleanRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		root = string(os.PathSeparator)
	}
	if !isRootedPath(root) {
		root = filepath.Join(string(os.PathSeparator), root)
	}
	root = filepath.Clean(root)
	if !isRootedPath(root) {
		return "", fmt.Errorf("memfs root %q must be absolute", root)
	}
	return root, nil
}

func isWithinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	parentPrefix := ".." + string(os.PathSeparator)
	return rel != ".." && !strings.HasPrefix(rel, parentPrefix)
}

func isRootedPath(path string) bool {
	if path == "" {
		return false
	}
	if filepath.IsAbs(path) {
		return true
	}
	return strings.HasPrefix(path, "/") || strings.HasPrefix(path, `\`)
}

func pathError(op, path string, err error) error {
	return &os.PathError{Op: op, Path: path, Err: err}
}

func newFIFOState() *fifoState {
	state := &fifoState{}
	state.cond = sync.NewCond(&state.mu)
	return state
}

type statInfo struct {
	name    string
	size    int64
	mode    fs.FileMode
	modTime time.Time
	atime   time.Time
	uid     uint32
	gid     uint32
	ino     uint64
}

func (i *statInfo) Name() string       { return i.name }
func (i *statInfo) Size() int64        { return i.size }
func (i *statInfo) Mode() fs.FileMode  { return i.mode }
func (i *statInfo) ModTime() time.Time { return i.modTime }
func (i *statInfo) IsDir() bool        { return i.mode.IsDir() }
func (i *statInfo) Sys() any {
	return &fileSysInfo{UID: i.uid, GID: i.gid, Inode: i.ino, AccessTime: i.atime}
}

type fileSysInfo struct {
	UID        uint32
	GID        uint32
	Inode      uint64
	AccessTime time.Time
}

type dirEntry struct {
	info *statInfo
}

func (d dirEntry) Name() string               { return d.info.Name() }
func (d dirEntry) IsDir() bool                { return d.info.IsDir() }
func (d dirEntry) Type() fs.FileMode          { return d.info.Mode().Type() }
func (d dirEntry) Info() (fs.FileInfo, error) { return d.info, nil }

type regularFile struct {
	fs       *FS
	path     string
	node     *node
	offset   int64
	readable bool
	writable bool
	append   bool
	closed   bool
}

func (f *regularFile) Stat() (fs.FileInfo, error) {
	f.fs.mu.RLock()
	defer f.fs.mu.RUnlock()
	return snapshotInfo(filepath.Base(f.path), f.node), nil
}

func (f *regularFile) Read(p []byte) (int, error) {
	if f.closed {
		return 0, fs.ErrClosed
	}
	if !f.readable {
		return 0, errBadFD
	}
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	if f.offset >= int64(len(f.node.data)) {
		f.node.atime = time.Now()
		return 0, io.EOF
	}
	n := copy(p, f.node.data[f.offset:])
	f.offset += int64(n)
	f.node.atime = time.Now()
	return n, nil
}

func (f *regularFile) Write(p []byte) (int, error) {
	if f.closed {
		return 0, fs.ErrClosed
	}
	if !f.writable {
		return 0, errBadFD
	}
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	if f.append {
		f.offset = int64(len(f.node.data))
	}
	end := int(f.offset) + len(p)
	if end > len(f.node.data) {
		grown := make([]byte, end)
		copy(grown, f.node.data)
		f.node.data = grown
	}
	copy(f.node.data[f.offset:], p)
	f.offset += int64(len(p))
	now := time.Now()
	f.node.atime = now
	f.node.mtime = now
	return len(p), nil
}

func (f *regularFile) Close() error {
	f.closed = true
	return nil
}

type dirFile struct {
	fs      *FS
	path    string
	node    *node
	entries []fs.DirEntry
	offset  int
	closed  bool
}

func (f *dirFile) Stat() (fs.FileInfo, error) {
	f.fs.mu.RLock()
	defer f.fs.mu.RUnlock()
	return snapshotInfo(filepath.Base(f.path), f.node), nil
}

func (f *dirFile) Read(_ []byte) (int, error) {
	return 0, errIsDirectory
}

func (f *dirFile) Close() error {
	f.closed = true
	return nil
}

func (f *dirFile) ReadDir(n int) ([]fs.DirEntry, error) {
	if f.closed {
		return nil, fs.ErrClosed
	}
	if f.entries == nil {
		f.fs.mu.RLock()
		f.entries = dirEntriesForNode(f.node)
		f.fs.mu.RUnlock()
	}
	if f.offset >= len(f.entries) && n > 0 {
		return nil, io.EOF
	}
	if n <= 0 || f.offset+n > len(f.entries) {
		n = len(f.entries) - f.offset
	}
	entries := append([]fs.DirEntry(nil), f.entries[f.offset:f.offset+n]...)
	f.offset += n
	return entries, nil
}

type fifoReadFile struct {
	fs     *FS
	path   string
	node   *node
	state  *fifoState
	closed bool
}

func (f *fifoReadFile) Stat() (fs.FileInfo, error) {
	f.fs.mu.RLock()
	defer f.fs.mu.RUnlock()
	return snapshotInfo(filepath.Base(f.path), f.node), nil
}

func (f *fifoReadFile) Read(p []byte) (int, error) {
	if f.closed {
		return 0, fs.ErrClosed
	}
	f.state.mu.Lock()
	defer f.state.mu.Unlock()
	for len(f.state.data) == 0 && !f.state.writerClosed {
		f.state.cond.Wait()
	}
	if len(f.state.data) == 0 && f.state.writerClosed {
		return 0, io.EOF
	}
	n := copy(p, f.state.data)
	f.state.data = f.state.data[n:]
	return n, nil
}

func (f *fifoReadFile) Write(_ []byte) (int, error) {
	return 0, errBadFD
}

func (f *fifoReadFile) Close() error {
	if f.closed {
		return nil
	}
	f.closed = true
	f.state.mu.Lock()
	f.state.readOpen = false
	f.state.readerClosed = true
	f.state.cond.Broadcast()
	f.state.mu.Unlock()
	return nil
}

type fifoWriteFile struct {
	fs     *FS
	path   string
	node   *node
	state  *fifoState
	closed bool
}

func (f *fifoWriteFile) Stat() (fs.FileInfo, error) {
	f.fs.mu.RLock()
	defer f.fs.mu.RUnlock()
	return snapshotInfo(filepath.Base(f.path), f.node), nil
}

func (f *fifoWriteFile) Read(_ []byte) (int, error) {
	return 0, errBadFD
}

func (f *fifoWriteFile) Write(p []byte) (int, error) {
	if f.closed {
		return 0, fs.ErrClosed
	}
	f.state.mu.Lock()
	defer f.state.mu.Unlock()
	if f.state.readerClosed {
		return 0, io.ErrClosedPipe
	}
	f.state.data = append(f.state.data, p...)
	f.state.cond.Broadcast()
	return len(p), nil
}

func (f *fifoWriteFile) Close() error {
	if f.closed {
		return nil
	}
	f.closed = true
	f.state.mu.Lock()
	f.state.writeOpen = false
	f.state.writerClosed = true
	f.state.cond.Broadcast()
	f.state.mu.Unlock()
	return nil
}
