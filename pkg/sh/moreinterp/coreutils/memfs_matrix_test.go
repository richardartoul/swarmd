package coreutils_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/richardartoul/swarmd/pkg/sh/interp"
	"github.com/richardartoul/swarmd/pkg/sh/memfs"
	"github.com/richardartoul/swarmd/pkg/sh/moreinterp/coreutils"
	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
	"github.com/richardartoul/swarmd/pkg/sh/syntax"
)

func TestFilesystemCommandsAcrossBackends(t *testing.T) {
	t.Parallel()

	diskRoot := t.TempDir()
	diskFS, err := sandbox.NewFS(diskRoot)
	if err != nil {
		t.Fatalf("sandbox.NewFS() error = %v", err)
	}
	diskDir, err := diskFS.Getwd()
	if err != nil {
		t.Fatalf("diskFS.Getwd() error = %v", err)
	}
	if err := diskFS.MkdirAll(diskFS.TempDir(), 0o755); err != nil {
		t.Fatalf("diskFS.MkdirAll(tmp) error = %v", err)
	}

	memRoot := "/workspace"
	memFS, err := memfs.New(memRoot)
	if err != nil {
		t.Fatalf("memfs.New() error = %v", err)
	}
	if err := memFS.MkdirAll(memFS.TempDir(), 0o755); err != nil {
		t.Fatalf("memFS.MkdirAll(tmp) error = %v", err)
	}

	tests := []struct {
		name string
		fsys sandbox.FileSystem
		root string
	}{
		{name: "disk", fsys: diskFS, root: diskDir},
		{name: "memfs", fsys: memFS, root: memRoot},
	}

	const script = "touch source.txt; mv source.txt moved.txt; cp moved.txt copy.txt; mktemp temp.XXXXXX"
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, err := runCoreutilsMatrixScript(t, tt.fsys, tt.root, script)
			if err != nil {
				t.Fatalf("Run() error = %v, stderr=%q", err, stderr)
			}
			tempPath := strings.TrimSpace(stdout)
			if !strings.HasPrefix(filepath.ToSlash(tempPath), filepath.ToSlash(filepath.Join(tt.root, ".tmp", "temp."))) {
				t.Fatalf("mktemp output = %q, want prefix %q", tempPath, filepath.Join(tt.root, ".tmp", "temp."))
			}
			if _, err := tt.fsys.Stat(filepath.Join(tt.root, "moved.txt")); err != nil {
				t.Fatalf("Stat(moved.txt) error = %v", err)
			}
			if _, err := tt.fsys.Stat(filepath.Join(tt.root, "copy.txt")); err != nil {
				t.Fatalf("Stat(copy.txt) error = %v", err)
			}
		})
	}
}

func runCoreutilsMatrixScript(t *testing.T, fsys interp.RunnerFileSystem, dir, script string) (stdout, stderr string, err error) {
	t.Helper()

	var outBuf, errBuf bytes.Buffer
	r, err := interp.New(
		interp.FileSystem(fsys),
		interp.Dir(dir),
		interp.StdIO(nil, &outBuf, &errBuf),
		interp.ExecHandlers(coreutils.ExecHandler),
	)
	if err != nil {
		t.Fatalf("interp.New() error = %v", err)
	}
	prog, err := syntax.NewParser().Parse(strings.NewReader(script), "")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	err = r.Run(context.Background(), prog)
	return outBuf.String(), errBuf.String(), err
}
