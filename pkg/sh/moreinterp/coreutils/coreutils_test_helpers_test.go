package coreutils

import (
	"context"
	"io"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/richardartoul/swarmd/pkg/sh/interp"
	"github.com/richardartoul/swarmd/pkg/sh/syntax"
)

type trackingFS struct {
	interp.OSFileSystem

	openCount      int
	readDirCount   int
	statCount      int
	lstatCount     int
	openFileCount  int
	removeCount    int
	mkdirCount     int
	mkdirAllCount  int
	removeAllCount int
	writeFileCount int
	linkCount      int
	symlinkCount   int
	chtimesCount   int
	readlinkCount  int
	renameCount    int
	chmodCount     int
}

func (fsys *trackingFS) Open(name string) (fs.File, error) {
	fsys.openCount++
	return fsys.OSFileSystem.Open(name)
}

func (fsys *trackingFS) ReadDir(name string) ([]fs.DirEntry, error) {
	fsys.readDirCount++
	return fsys.OSFileSystem.ReadDir(name)
}

func (fsys *trackingFS) Stat(name string) (fs.FileInfo, error) {
	fsys.statCount++
	return fsys.OSFileSystem.Stat(name)
}

func (fsys *trackingFS) Lstat(name string) (fs.FileInfo, error) {
	fsys.lstatCount++
	return fsys.OSFileSystem.Lstat(name)
}

func (fsys *trackingFS) OpenFile(name string, flag int, perm os.FileMode) (io.ReadWriteCloser, error) {
	fsys.openFileCount++
	return fsys.OSFileSystem.OpenFile(name, flag, perm)
}

func (fsys *trackingFS) Remove(name string) error {
	fsys.removeCount++
	return fsys.OSFileSystem.Remove(name)
}

func (fsys *trackingFS) Mkdir(name string, perm os.FileMode) error {
	fsys.mkdirCount++
	return fsys.OSFileSystem.Mkdir(name, perm)
}

func (fsys *trackingFS) MkdirAll(path string, perm os.FileMode) error {
	fsys.mkdirAllCount++
	return fsys.OSFileSystem.MkdirAll(path, perm)
}

func (fsys *trackingFS) RemoveAll(path string) error {
	fsys.removeAllCount++
	return fsys.OSFileSystem.RemoveAll(path)
}

func (fsys *trackingFS) WriteFile(name string, data []byte, perm os.FileMode) error {
	fsys.writeFileCount++
	return fsys.OSFileSystem.WriteFile(name, data, perm)
}

func (fsys *trackingFS) Link(oldname, newname string) error {
	fsys.linkCount++
	return fsys.OSFileSystem.Link(oldname, newname)
}

func (fsys *trackingFS) Symlink(oldname, newname string) error {
	fsys.symlinkCount++
	return fsys.OSFileSystem.Symlink(oldname, newname)
}

func (fsys *trackingFS) Chtimes(name string, atime, mtime time.Time) error {
	fsys.chtimesCount++
	return fsys.OSFileSystem.Chtimes(name, atime, mtime)
}

func (fsys *trackingFS) Readlink(name string) (string, error) {
	fsys.readlinkCount++
	return fsys.OSFileSystem.Readlink(name)
}

func (fsys *trackingFS) Rename(oldpath, newpath string) error {
	fsys.renameCount++
	return fsys.OSFileSystem.Rename(oldpath, newpath)
}

func (fsys *trackingFS) Chmod(name string, mode os.FileMode) error {
	fsys.chmodCount++
	return fsys.OSFileSystem.Chmod(name, mode)
}

func runCoreutilsScript(t *testing.T, fsys interp.RunnerFileSystem, dir, stdin, script string) (string, string, error) {
	return runCoreutilsScriptWithOptions(t, fsys, dir, stdin, script)
}

func runCoreutilsScriptWithOptions(t *testing.T, fsys interp.RunnerFileSystem, dir, stdin, script string, opts ...interp.RunnerOption) (string, string, error) {
	t.Helper()
	var stdout, stderr strings.Builder
	runnerOpts := []interp.RunnerOption{
		interp.Dir(dir),
		interp.FileSystem(fsys),
		interp.StdIO(strings.NewReader(stdin), &stdout, &stderr),
		interp.ExecHandlers(ExecHandler),
	}
	runnerOpts = append(runnerOpts, opts...)
	r, err := interp.New(runnerOpts...)
	if err != nil {
		t.Fatalf("failed to create interpreter: %v", err)
	}
	program, err := syntax.NewParser().Parse(strings.NewReader(script), "")
	if err != nil {
		t.Fatalf("failed to parse %q: %v", script, err)
	}
	err = r.Run(context.Background(), program)
	return stdout.String(), stderr.String(), err
}
