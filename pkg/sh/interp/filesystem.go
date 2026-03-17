// See LICENSE for licensing information

package interp

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

var ErrNoHostPath = errors.New("filesystem path has no host representation")

// ReadFileSystem groups read-only filesystem capabilities.
//
// Paths follow host filesystem semantics, including absolute paths.
type ReadFileSystem interface {
	fs.FS
	fs.ReadDirFS
	fs.StatFS
}

// LstatFileSystem extends [ReadFileSystem] with symlink-aware stat support.
type LstatFileSystem interface {
	Lstat(name string) (fs.FileInfo, error)
}

// OpenFileSystem extends [ReadFileSystem] with flag-driven file open support.
type OpenFileSystem interface {
	OpenFile(name string, flag int, perm os.FileMode) (io.ReadWriteCloser, error)
}

// RemoveFileSystem extends [ReadFileSystem] with single-path remove support.
type RemoveFileSystem interface {
	Remove(name string) error
}

// ReadlinkFileSystem extends [ReadFileSystem] with symlink target reads.
type ReadlinkFileSystem interface {
	Readlink(name string) (string, error)
}

// RenameFileSystem extends [ReadFileSystem] with rename support.
type RenameFileSystem interface {
	Rename(oldpath, newpath string) error
}

// ChmodFileSystem extends [ReadFileSystem] with mode mutation support.
type ChmodFileSystem interface {
	Chmod(name string, mode os.FileMode) error
}

// MutableFileSystem groups filesystem mutation operations used across the repository.
type MutableFileSystem interface {
	Mkdir(name string, perm os.FileMode) error
	MkdirAll(path string, perm os.FileMode) error
	RemoveAll(path string) error
	WriteFile(name string, data []byte, perm os.FileMode) error
	Link(oldname, newname string) error
	Symlink(oldname, newname string) error
	Chtimes(name string, atime, mtime time.Time) error
}

// WorkingDirFileSystem can report the process working directory.
type WorkingDirFileSystem interface {
	Getwd() (string, error)
}

// TempDirFileSystem can report the process temporary directory.
type TempDirFileSystem interface {
	TempDir() string
}

// ResolvePathFileSystem can resolve a path relative to a working directory
// using filesystem-specific semantics.
type ResolvePathFileSystem interface {
	ResolvePath(dir, path string) (string, error)
}

// AccessFileSystem can check read/write/execute access using filesystem-owned
// metadata instead of the host OS.
type AccessFileSystem interface {
	Access(name string, mode uint32) error
}

// SameFileFileSystem can determine whether two file infos refer to the same
// underlying file.
type SameFileFileSystem interface {
	SameFile(firstPath string, firstInfo fs.FileInfo, secondPath string, secondInfo fs.FileInfo) bool
}

// OwnerIDFileSystem can report owner and group ids from filesystem-specific
// metadata.
type OwnerIDFileSystem interface {
	OwnerIDs(info fs.FileInfo) (uid, gid uint32, ok bool)
}

// EvalSymlinksFileSystem can resolve symbolic links using filesystem-owned
// path semantics.
type EvalSymlinksFileSystem interface {
	EvalSymlinks(path string) (string, error)
}

// MkfifoFileSystem can create a named pipe using filesystem-owned behavior.
type MkfifoFileSystem interface {
	Mkfifo(path string, mode uint32) error
}

// HostPathFileSystem can map a shell-visible path to a host path suitable for
// APIs like [os/exec.Cmd].
type HostPathFileSystem interface {
	HostPath(path string) (string, error)
}

const (
	AccessRead    uint32 = 0x4
	AccessWrite   uint32 = 0x2
	AccessExecute uint32 = 0x1
)

// RunnerFileSystem is the filesystem contract used by [Runner].
type RunnerFileSystem interface {
	ReadFileSystem
	LstatFileSystem
	OpenFileSystem
	RemoveFileSystem
}

// OSFileSystem is the default host filesystem adapter.
type OSFileSystem struct{}

var _ RunnerFileSystem = OSFileSystem{}
var _ MutableFileSystem = OSFileSystem{}
var _ WorkingDirFileSystem = OSFileSystem{}
var _ TempDirFileSystem = OSFileSystem{}
var _ ReadlinkFileSystem = OSFileSystem{}
var _ RenameFileSystem = OSFileSystem{}
var _ ChmodFileSystem = OSFileSystem{}

// DefaultRunnerFileSystem returns the default host-backed filesystem.
func DefaultRunnerFileSystem() RunnerFileSystem {
	return OSFileSystem{}
}

// IsDevNullPath reports whether name targets the null device path recognized by the shell.
func IsDevNullPath(name string) bool {
	return name == "/dev/null" || name == os.DevNull
}

func resolveOSPath(name string, flag int) (string, int) {
	if runtime.GOOS == "windows" && IsDevNullPath(name) {
		name = os.DevNull
		// Note that even though https://go.dev/issue/71752 was resolved for Windows,
		// the workaround here seems to still be required for Wine as of 10.14.
		// TODO(mvdan): Why? Is this Wine's fault?
		flag &^= os.O_TRUNC
	}
	return name, flag
}

func (OSFileSystem) Open(name string) (fs.File, error) {
	name, _ = resolveOSPath(name, 0)
	return os.Open(name)
}

func (OSFileSystem) ReadDir(name string) ([]fs.DirEntry, error) {
	return os.ReadDir(name)
}

func (OSFileSystem) Stat(name string) (fs.FileInfo, error) {
	name, _ = resolveOSPath(name, 0)
	return os.Stat(name)
}

func (OSFileSystem) Lstat(name string) (fs.FileInfo, error) {
	name, _ = resolveOSPath(name, 0)
	return os.Lstat(name)
}

func (OSFileSystem) OpenFile(name string, flag int, perm os.FileMode) (io.ReadWriteCloser, error) {
	name, flag = resolveOSPath(name, flag)
	return os.OpenFile(name, flag, perm)
}

func (OSFileSystem) Remove(name string) error {
	return os.Remove(name)
}

func (OSFileSystem) Readlink(name string) (string, error) {
	return os.Readlink(name)
}

func (OSFileSystem) Rename(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}

func (OSFileSystem) Chmod(name string, mode os.FileMode) error {
	return os.Chmod(name, mode)
}

func (OSFileSystem) Mkdir(name string, perm os.FileMode) error {
	return os.Mkdir(name, perm)
}

func (OSFileSystem) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

func (OSFileSystem) RemoveAll(path string) error {
	return os.RemoveAll(path)
}

func (OSFileSystem) WriteFile(name string, data []byte, perm os.FileMode) error {
	return os.WriteFile(name, data, perm)
}

func (OSFileSystem) Link(oldname, newname string) error {
	return os.Link(oldname, newname)
}

func (OSFileSystem) Symlink(oldname, newname string) error {
	return os.Symlink(oldname, newname)
}

func (OSFileSystem) Chtimes(name string, atime, mtime time.Time) error {
	return os.Chtimes(name, atime, mtime)
}

func (OSFileSystem) Getwd() (string, error) {
	return os.Getwd()
}

func (OSFileSystem) TempDir() string {
	return os.TempDir()
}

func (OSFileSystem) SameFile(firstPath string, firstInfo fs.FileInfo, secondPath string, secondInfo fs.FileInfo) bool {
	return os.SameFile(firstInfo, secondInfo)
}

func (OSFileSystem) EvalSymlinks(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}

func (OSFileSystem) HostPath(path string) (string, error) {
	if path == "" {
		return "", ErrNoHostPath
	}
	return filepath.Clean(path), nil
}

func fsGetwd(fsys RunnerFileSystem) (string, error) {
	if fsys2, ok := fsys.(WorkingDirFileSystem); ok {
		return fsys2.Getwd()
	}
	return os.Getwd()
}

func fsTempDir(fsys RunnerFileSystem) string {
	if fsys2, ok := fsys.(TempDirFileSystem); ok {
		return fsys2.TempDir()
	}
	return os.TempDir()
}

// ResolvePath resolves path relative to dir using any filesystem-specific
// rules before falling back to lexical host-style resolution.
func ResolvePath(fsys RunnerFileSystem, dir, path string) (string, error) {
	if IsDevNullPath(path) {
		return path, nil
	}
	if fsys2, ok := fsys.(ResolvePathFileSystem); ok {
		return fsys2.ResolvePath(dir, path)
	}
	return resolvePath(dir, path), nil
}

func fsSameFile(fsys RunnerFileSystem, firstPath string, firstInfo fs.FileInfo, secondPath string, secondInfo fs.FileInfo) bool {
	if fsys2, ok := fsys.(SameFileFileSystem); ok {
		return fsys2.SameFile(firstPath, firstInfo, secondPath, secondInfo)
	}
	return os.SameFile(firstInfo, secondInfo)
}

// SameFile reports whether two paths and file infos identify the same file
// using any filesystem-specific identity rules first.
func SameFile(fsys RunnerFileSystem, firstPath string, firstInfo fs.FileInfo, secondPath string, secondInfo fs.FileInfo) bool {
	return fsSameFile(fsys, firstPath, firstInfo, secondPath, secondInfo)
}

func fsOwnerIDs(fsys RunnerFileSystem, info fs.FileInfo) (uid, gid uint32, ok bool) {
	if fsys2, ok := fsys.(OwnerIDFileSystem); ok {
		return fsys2.OwnerIDs(info)
	}
	return 0, 0, false
}

func fsEvalSymlinks(fsys RunnerFileSystem, path string) (string, error) {
	if fsys2, ok := fsys.(EvalSymlinksFileSystem); ok {
		return fsys2.EvalSymlinks(path)
	}
	return filepath.EvalSymlinks(path)
}

func fsMkfifo(fsys RunnerFileSystem, path string, mode uint32) error {
	if fsys2, ok := fsys.(MkfifoFileSystem); ok {
		return fsys2.Mkfifo(path, mode)
	}
	return mkfifo(path, mode)
}

func fsHostPath(fsys RunnerFileSystem, path string) (string, error) {
	if fsys2, ok := fsys.(HostPathFileSystem); ok {
		return fsys2.HostPath(path)
	}
	return "", ErrNoHostPath
}

func fsExecAvailable(fsys RunnerFileSystem, dir, path string) bool {
	if _, err := fsHostPath(fsys, dir); err != nil {
		return false
	}
	if _, err := fsHostPath(fsys, path); err != nil {
		return false
	}
	return true
}

// resolvePath resolves path relative to dir when needed and cleans it.
func resolvePath(dir, path string) string {
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	return filepath.Clean(path)
}
