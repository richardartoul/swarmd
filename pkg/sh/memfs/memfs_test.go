package memfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newTestFS(t *testing.T) *FS {
	t.Helper()
	fsys, err := New("/sandbox")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return fsys
}

func mustWriteFile(t *testing.T, fsys *FS, name, content string) {
	t.Helper()
	if err := fsys.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(name), err)
	}
	if err := fsys.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", name, err)
	}
}

func mustReadFile(t *testing.T, fsys *FS, name string) string {
	t.Helper()
	file, err := fsys.Open(name)
	if err != nil {
		t.Fatalf("Open(%q) error = %v", name, err)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("ReadAll(%q) error = %v", name, err)
	}
	return string(data)
}

func TestResolvePathStaysWithinRoot(t *testing.T) {
	t.Parallel()

	fsys := newTestFS(t)
	tests := []struct {
		name    string
		dir     string
		path    string
		want    string
		wantErr bool
	}{
		{name: "relative joins cwd", dir: "/sandbox/sub", path: "file.txt", want: "/sandbox/sub/file.txt"},
		{name: "absolute inside root", dir: "", path: "/sandbox/a/b", want: "/sandbox/a/b"},
		{name: "dot dot collapses inside root", dir: "/sandbox/a", path: "../b", want: "/sandbox/b"},
		{name: "escape via dot dot rejected", dir: "/sandbox", path: "../outside", wantErr: true},
		{name: "absolute outside root rejected", dir: "", path: "/etc/passwd", wantErr: true},
		{name: "empty path resolves dir", dir: "/sandbox/a", path: "", want: "/sandbox/a"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := fsys.ResolvePath(test.dir, test.path)
			if test.wantErr {
				if err == nil {
					t.Fatalf("ResolvePath(%q, %q) = %q, want error", test.dir, test.path, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolvePath(%q, %q) error = %v", test.dir, test.path, err)
			}
			if got != test.want {
				t.Fatalf("ResolvePath(%q, %q) = %q, want %q", test.dir, test.path, got, test.want)
			}
		})
	}
}

func TestOpenFileFlagMatrix(t *testing.T) {
	t.Parallel()

	t.Run("create excl fails on existing", func(t *testing.T) {
		t.Parallel()
		fsys := newTestFS(t)
		mustWriteFile(t, fsys, "/sandbox/f.txt", "data")
		_, err := fsys.OpenFile("/sandbox/f.txt", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if !errors.Is(err, fs.ErrExist) {
			t.Fatalf("OpenFile(O_CREATE|O_EXCL) error = %v, want fs.ErrExist", err)
		}
	})

	t.Run("truncate clears content", func(t *testing.T) {
		t.Parallel()
		fsys := newTestFS(t)
		mustWriteFile(t, fsys, "/sandbox/f.txt", "old content")
		file, err := fsys.OpenFile("/sandbox/f.txt", os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			t.Fatalf("OpenFile(O_TRUNC) error = %v", err)
		}
		if _, err := file.Write([]byte("new")); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		if got := mustReadFile(t, fsys, "/sandbox/f.txt"); got != "new" {
			t.Fatalf("content = %q, want %q", got, "new")
		}
	})

	t.Run("append writes at end", func(t *testing.T) {
		t.Parallel()
		fsys := newTestFS(t)
		mustWriteFile(t, fsys, "/sandbox/f.txt", "start-")
		file, err := fsys.OpenFile("/sandbox/f.txt", os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			t.Fatalf("OpenFile(O_APPEND) error = %v", err)
		}
		if _, err := file.Write([]byte("end")); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		if got := mustReadFile(t, fsys, "/sandbox/f.txt"); got != "start-end" {
			t.Fatalf("content = %q, want %q", got, "start-end")
		}
	})

	t.Run("missing file without create fails", func(t *testing.T) {
		t.Parallel()
		fsys := newTestFS(t)
		_, err := fsys.OpenFile("/sandbox/missing.txt", os.O_WRONLY, 0o644)
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("OpenFile(missing) error = %v, want fs.ErrNotExist", err)
		}
	})

	t.Run("open directory for write fails", func(t *testing.T) {
		t.Parallel()
		fsys := newTestFS(t)
		if err := fsys.Mkdir("/sandbox/dir", 0o755); err != nil {
			t.Fatalf("Mkdir() error = %v", err)
		}
		if _, err := fsys.OpenFile("/sandbox/dir", os.O_WRONLY, 0o644); err == nil {
			t.Fatal("OpenFile(directory) error = nil, want is-a-directory failure")
		}
	})
}

func TestStatFollowsSymlinksAndLstatDoesNot(t *testing.T) {
	t.Parallel()

	fsys := newTestFS(t)
	mustWriteFile(t, fsys, "/sandbox/target.txt", "content")
	if err := fsys.Symlink("target.txt", "/sandbox/link"); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	statInfo, err := fsys.Stat("/sandbox/link")
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if statInfo.Mode()&fs.ModeSymlink != 0 {
		t.Fatalf("Stat() mode = %v, want symlink followed", statInfo.Mode())
	}
	if statInfo.Size() != int64(len("content")) {
		t.Fatalf("Stat() size = %d, want %d", statInfo.Size(), len("content"))
	}

	lstatInfo, err := fsys.Lstat("/sandbox/link")
	if err != nil {
		t.Fatalf("Lstat() error = %v", err)
	}
	if lstatInfo.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("Lstat() mode = %v, want symlink not followed", lstatInfo.Mode())
	}

	target, err := fsys.Readlink("/sandbox/link")
	if err != nil {
		t.Fatalf("Readlink() error = %v", err)
	}
	if target != "target.txt" {
		t.Fatalf("Readlink() = %q, want %q", target, "target.txt")
	}
}

func TestSymlinkLoopsAreBounded(t *testing.T) {
	t.Parallel()

	fsys := newTestFS(t)
	if err := fsys.Symlink("b", "/sandbox/a"); err != nil {
		t.Fatalf("Symlink(a) error = %v", err)
	}
	if err := fsys.Symlink("a", "/sandbox/b"); err != nil {
		t.Fatalf("Symlink(b) error = %v", err)
	}
	if _, err := fsys.Stat("/sandbox/a"); err == nil {
		t.Fatal("Stat() on symlink loop error = nil, want loop failure")
	}
}

func TestRenameMovesAndReplaces(t *testing.T) {
	t.Parallel()

	fsys := newTestFS(t)
	mustWriteFile(t, fsys, "/sandbox/src.txt", "payload")
	mustWriteFile(t, fsys, "/sandbox/dst.txt", "old payload")
	if err := fsys.Rename("/sandbox/src.txt", "/sandbox/dst.txt"); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	if got := mustReadFile(t, fsys, "/sandbox/dst.txt"); got != "payload" {
		t.Fatalf("dst content = %q, want %q", got, "payload")
	}
	if _, err := fsys.Lstat("/sandbox/src.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Lstat(src) error = %v, want fs.ErrNotExist", err)
	}
}

// TestRenameOntoSelfIsNoOp pins POSIX rename(2) semantics: renaming a path
// onto itself (directly or via a hard link) succeeds and changes nothing.
// The previous implementation routed self-renames through the replacement
// path and corrupted the file's link count.
func TestRenameOntoSelfIsNoOp(t *testing.T) {
	t.Parallel()

	fsys := newTestFS(t)
	mustWriteFile(t, fsys, "/sandbox/f.txt", "stable")
	if err := fsys.Rename("/sandbox/f.txt", "/sandbox/f.txt"); err != nil {
		t.Fatalf("Rename(self) error = %v", err)
	}
	if got := mustReadFile(t, fsys, "/sandbox/f.txt"); got != "stable" {
		t.Fatalf("content = %q, want %q", got, "stable")
	}

	if err := fsys.Link("/sandbox/f.txt", "/sandbox/hard.txt"); err != nil {
		t.Fatalf("Link() error = %v", err)
	}
	if err := fsys.Rename("/sandbox/f.txt", "/sandbox/hard.txt"); err != nil {
		t.Fatalf("Rename(hard link alias) error = %v", err)
	}
	// Both links must survive, per POSIX.
	if got := mustReadFile(t, fsys, "/sandbox/f.txt"); got != "stable" {
		t.Fatalf("f.txt content = %q, want %q", got, "stable")
	}
	if got := mustReadFile(t, fsys, "/sandbox/hard.txt"); got != "stable" {
		t.Fatalf("hard.txt content = %q, want %q", got, "stable")
	}
}

// TestRenameRejectsMovingDirectoryIntoItself pins the fix for a tree
// corruption bug: moving a directory into its own subtree detached the
// directory from the tree, silently destroying every file under it.
func TestRenameRejectsMovingDirectoryIntoItself(t *testing.T) {
	t.Parallel()

	fsys := newTestFS(t)
	mustWriteFile(t, fsys, "/sandbox/dir/sub/file.txt", "precious")
	if err := fsys.Rename("/sandbox/dir", "/sandbox/dir/sub/dir"); err == nil {
		t.Fatal("Rename(dir into own subtree) error = nil, want EINVAL-style failure")
	}
	// The tree must be intact.
	if got := mustReadFile(t, fsys, "/sandbox/dir/sub/file.txt"); got != "precious" {
		t.Fatalf("content after rejected rename = %q, want %q", got, "precious")
	}
}

func TestRenameTypeMismatches(t *testing.T) {
	t.Parallel()

	fsys := newTestFS(t)
	mustWriteFile(t, fsys, "/sandbox/file.txt", "data")
	if err := fsys.Mkdir("/sandbox/emptydir", 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}

	if err := fsys.Rename("/sandbox/file.txt", "/sandbox/emptydir"); err == nil {
		t.Fatal("Rename(file onto directory) error = nil, want failure")
	}
	if err := fsys.Rename("/sandbox/emptydir", "/sandbox/file.txt"); err == nil {
		t.Fatal("Rename(directory onto file) error = nil, want failure")
	}

	mustWriteFile(t, fsys, "/sandbox/nonempty/inner.txt", "x")
	if err := fsys.Mkdir("/sandbox/otherdir", 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := fsys.Rename("/sandbox/otherdir", "/sandbox/nonempty"); err == nil {
		t.Fatal("Rename(dir onto non-empty dir) error = nil, want failure")
	}
}

func TestRemoveSemantics(t *testing.T) {
	t.Parallel()

	fsys := newTestFS(t)
	mustWriteFile(t, fsys, "/sandbox/dir/file.txt", "x")

	if err := fsys.Remove("/sandbox/dir"); err == nil {
		t.Fatal("Remove(non-empty dir) error = nil, want failure")
	}
	if err := fsys.Remove("/sandbox/dir/file.txt"); err != nil {
		t.Fatalf("Remove(file) error = %v", err)
	}
	if err := fsys.Remove("/sandbox/dir"); err != nil {
		t.Fatalf("Remove(empty dir) error = %v", err)
	}
	if err := fsys.RemoveAll("/sandbox/missing"); err != nil {
		t.Fatalf("RemoveAll(missing) error = %v, want nil", err)
	}

	mustWriteFile(t, fsys, "/sandbox/tree/a/b/c.txt", "deep")
	if err := fsys.RemoveAll("/sandbox/tree"); err != nil {
		t.Fatalf("RemoveAll(tree) error = %v", err)
	}
	if _, err := fsys.Lstat("/sandbox/tree"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Lstat(tree) error = %v, want fs.ErrNotExist", err)
	}
}

func TestHardLinksShareContent(t *testing.T) {
	t.Parallel()

	fsys := newTestFS(t)
	mustWriteFile(t, fsys, "/sandbox/a.txt", "v1")
	if err := fsys.Link("/sandbox/a.txt", "/sandbox/b.txt"); err != nil {
		t.Fatalf("Link() error = %v", err)
	}
	if err := fsys.WriteFile("/sandbox/a.txt", []byte("v2"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if got := mustReadFile(t, fsys, "/sandbox/b.txt"); got != "v2" {
		t.Fatalf("hard link content = %q, want %q", got, "v2")
	}

	aInfo, err := fsys.Stat("/sandbox/a.txt")
	if err != nil {
		t.Fatalf("Stat(a) error = %v", err)
	}
	bInfo, err := fsys.Stat("/sandbox/b.txt")
	if err != nil {
		t.Fatalf("Stat(b) error = %v", err)
	}
	if !fsys.SameFile("/sandbox/a.txt", aInfo, "/sandbox/b.txt", bInfo) {
		t.Fatal("SameFile() = false, want true for hard links")
	}
}

func TestMkdirAllAndReadDirOrdering(t *testing.T) {
	t.Parallel()

	fsys := newTestFS(t)
	for _, name := range []string{"zeta", "alpha", "mid"} {
		mustWriteFile(t, fsys, "/sandbox/dir/"+name, "x")
	}
	entries, err := fsys.ReadDir("/sandbox/dir")
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if got, want := strings.Join(names, ","), "alpha,mid,zeta"; got != want {
		t.Fatalf("ReadDir() names = %q, want %q (sorted)", got, want)
	}

	// MkdirAll over an existing file must fail.
	if err := fsys.MkdirAll("/sandbox/dir/alpha/deeper", 0o755); err == nil {
		t.Fatal("MkdirAll(through file) error = nil, want failure")
	}
}

func TestConcurrentAccessIsSafe(t *testing.T) {
	t.Parallel()

	fsys := newTestFS(t)
	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dir := fmt.Sprintf("/sandbox/w%d", worker)
			for iter := range 50 {
				name := fmt.Sprintf("%s/f%d.txt", dir, iter)
				if err := fsys.MkdirAll(dir, 0o755); err != nil {
					t.Errorf("MkdirAll(%q) error = %v", dir, err)
					return
				}
				if err := fsys.WriteFile(name, []byte("data"), 0o644); err != nil {
					t.Errorf("WriteFile(%q) error = %v", name, err)
					return
				}
				if _, err := fsys.Stat(name); err != nil {
					t.Errorf("Stat(%q) error = %v", name, err)
					return
				}
				if _, err := fsys.ReadDir(dir); err != nil {
					t.Errorf("ReadDir(%q) error = %v", dir, err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
