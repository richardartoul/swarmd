package interp_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/richardartoul/swarmd/pkg/sh/expand"
	"github.com/richardartoul/swarmd/pkg/sh/interp"
	"github.com/richardartoul/swarmd/pkg/sh/memfs"
	"github.com/richardartoul/swarmd/pkg/sh/moreinterp/coreutils"
	"github.com/richardartoul/swarmd/pkg/sh/syntax"
)

func TestMemFSDirResolvesRelativeToFilesystemRoot(t *testing.T) {
	t.Parallel()

	fsys, err := memfs.New("/workspace")
	if err != nil {
		t.Fatalf("memfs.New() error = %v", err)
	}
	if err := fsys.MkdirAll("/workspace/subdir", 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	r, err := interp.New(
		interp.FileSystem(fsys),
		interp.Dir("subdir"),
	)
	if err != nil {
		t.Fatalf("interp.New() error = %v", err)
	}
	if got := r.Dir; got != "/workspace/subdir" {
		t.Fatalf("runner.Dir = %q, want %q", got, "/workspace/subdir")
	}
}

func TestPwdPhysicalUsesFilesystemSymlinkResolution(t *testing.T) {
	t.Parallel()

	diskRoot := t.TempDir()
	diskReal := filepath.Join(diskRoot, "real")
	diskLink := filepath.Join(diskRoot, "link")
	if err := os.MkdirAll(diskReal, 0o755); err != nil {
		t.Fatalf("os.MkdirAll() error = %v", err)
	}
	if err := os.Symlink(diskReal, diskLink); err != nil {
		t.Fatalf("os.Symlink() error = %v", err)
	}
	diskResolved, err := filepath.EvalSymlinks(diskLink)
	if err != nil {
		t.Fatalf("filepath.EvalSymlinks() error = %v", err)
	}

	memRoot := "/workspace"
	memReal := "/workspace/real"
	memLink := "/workspace/link"
	mem, err := memfs.New(memRoot)
	if err != nil {
		t.Fatalf("memfs.New() error = %v", err)
	}
	if err := mem.MkdirAll(memReal, 0o755); err != nil {
		t.Fatalf("mem.MkdirAll() error = %v", err)
	}
	if err := mem.Symlink(memReal, memLink); err != nil {
		t.Fatalf("mem.Symlink() error = %v", err)
	}

	tests := []struct {
		name string
		fsys interp.RunnerFileSystem
		dir  string
		want string
	}{
		{name: "disk", fsys: interp.OSFileSystem{}, dir: diskLink, want: diskResolved + "\n"},
		{name: "memfs", fsys: mem, dir: memLink, want: memReal + "\n"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, err := runInterpScript(t, tt.fsys, tt.dir, "pwd -P")
			if err != nil {
				t.Fatalf("Run() error = %v, stderr=%q", err, stderr)
			}
			if stdout != tt.want {
				t.Fatalf("stdout = %q, want %q", stdout, tt.want)
			}
		})
	}
}

func TestCommandDiscoveryRequiresExecutableBackend(t *testing.T) {
	t.Parallel()

	diskRoot := t.TempDir()
	diskBin := filepath.Join(diskRoot, "bin")
	diskTool := filepath.Join(diskBin, "tool")
	if err := os.MkdirAll(diskBin, 0o755); err != nil {
		t.Fatalf("os.MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(diskTool, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	mem, err := memfs.New("/workspace")
	if err != nil {
		t.Fatalf("memfs.New() error = %v", err)
	}
	if err := mem.MkdirAll("/workspace/bin", 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := mem.WriteFile("/workspace/bin/tool", []byte("virtual"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	tests := []struct {
		name string
		fsys interp.RunnerFileSystem
		dir  string
		path string
		want string
	}{
		{
			name: "disk",
			fsys: interp.OSFileSystem{},
			dir:  diskRoot,
			path: diskBin,
			want: "type-ok\ncommand-ok\n",
		},
		{
			name: "memfs",
			fsys: mem,
			dir:  "/workspace",
			path: "/workspace/bin",
			want: "type-miss\ncommand-miss\n",
		},
	}
	const script = "type -P tool >/dev/null && echo type-ok || echo type-miss; command -v tool >/dev/null && echo command-ok || echo command-miss"
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, err := runInterpScript(
				t,
				tt.fsys,
				tt.dir,
				script,
				interp.Env(expand.ListEnviron("PATH="+tt.path)),
			)
			if err != nil {
				t.Fatalf("Run() error = %v, stderr=%q", err, stderr)
			}
			if stdout != tt.want {
				t.Fatalf("stdout = %q, want %q", stdout, tt.want)
			}
		})
	}
}

func TestMemFSDefaultExecFailsFastWithoutHostPath(t *testing.T) {
	t.Parallel()

	fsys, err := memfs.New("/workspace")
	if err != nil {
		t.Fatalf("memfs.New() error = %v", err)
	}
	if err := fsys.MkdirAll("/workspace/bin", 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := fsys.WriteFile("/workspace/bin/tool", []byte("virtual"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	stdout, stderr, err := runInterpScript(
		t,
		fsys,
		"/workspace",
		"tool",
		interp.Env(expand.ListEnviron("PATH=/workspace/bin")),
	)
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	var exit interp.ExitStatus
	if !errors.As(err, &exit) || exit != 126 {
		t.Fatalf("Run() error = %v, want exit status 126", err)
	}
	if !strings.Contains(stderr, "external commands unavailable with current filesystem backend") {
		t.Fatalf("stderr = %q, want host-exec error", stderr)
	}
}

func TestMemFSProcessSubstitutionWithInProcessCommands(t *testing.T) {
	t.Parallel()

	fsys, err := memfs.New("/workspace")
	if err != nil {
		t.Fatalf("memfs.New() error = %v", err)
	}

	stdout, stderr, err := runInterpScript(
		t,
		fsys,
		"/workspace",
		"cat <(printf foo)",
		interp.ExecHandlers(coreutils.ExecHandler),
	)
	if err != nil {
		t.Fatalf("Run() error = %v, stderr=%q", err, stderr)
	}
	if stdout != "foo" {
		t.Fatalf("stdout = %q, want %q", stdout, "foo")
	}
}

func runInterpScript(
	t *testing.T,
	fsys interp.RunnerFileSystem,
	dir, script string,
	opts ...interp.RunnerOption,
) (stdout, stderr string, err error) {
	t.Helper()

	var outBuf, errBuf bytes.Buffer
	allOpts := []interp.RunnerOption{
		interp.FileSystem(fsys),
		interp.Dir(dir),
		interp.StdIO(nil, &outBuf, &errBuf),
	}
	allOpts = append(allOpts, opts...)
	r, err := interp.New(allOpts...)
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
