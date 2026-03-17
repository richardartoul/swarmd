// Copyright (c) 2025, Andrey Nering <andrey@nering.com.br>
// See LICENSE for licensing information

package coreutils

import (
	archivezip "archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/richardartoul/swarmd/pkg/sh/expand"
	"github.com/richardartoul/swarmd/pkg/sh/internal"
	"github.com/richardartoul/swarmd/pkg/sh/interp"
	"github.com/richardartoul/swarmd/pkg/sh/syntax"
)

func TestExecHandlerRejectsUnknownFlags(t *testing.T) {
	for _, coreUtil := range commandNames {
		t.Run(coreUtil, func(t *testing.T) {
			var in bytes.Buffer
			var out strings.Builder

			r, err := interp.New(
				interp.StdIO(&in, &out, &out),
				interp.ExecHandlers(ExecHandler),
			)
			if err != nil {
				t.Fatalf("failed to create interpreter: %v", err)
			}

			cmd := fmt.Sprintf("%s --badoption", coreUtil)

			program, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
			if err != nil {
				t.Fatalf("failed to parse command %q: %v", cmd, err)
			}
			err = r.Run(context.Background(), program)
			if err == nil {
				t.Fatalf("expected error for command %q, got none", cmd)
			}

			if !strings.Contains(err.Error(), "flag provided but not defined: -badoption") {
				t.Errorf("expected error for command %q, got: %v", cmd, err)
			}
		})
	}
}

func TestParseUtilityOptionsFormatsErrors(t *testing.T) {
	t.Parallel()

	_, _, err := parseUtilityOptions([]string{"--badoption"}, []internal.OptionSpec{
		{Canonical: "a", Names: []string{"-a"}},
	})
	if err == nil {
		t.Fatal("expected unknown option error")
	}
	if got := err.Error(); got != "flag provided but not defined: -badoption" {
		t.Fatalf("unknown option error = %q, want %q", got, "flag provided but not defined: -badoption")
	}

	_, _, err = parseUtilityOptions([]string{"-o"}, []internal.OptionSpec{
		{Canonical: "output", Names: []string{"-o"}, ValueMode: internal.RequiredOptionValue},
	})
	if err == nil {
		t.Fatal("expected missing value error")
	}
	if got := err.Error(); got != "flag needs an argument: -o" {
		t.Fatalf("missing value error = %q, want %q", got, "flag needs an argument: -o")
	}
}

func TestCommandsUseFilesystemAdapter(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(t *testing.T, dir string)
		opts   []interp.RunnerOption
		stdin  string
		script string
		assert func(t *testing.T, dir string, fsys *trackingFS, stdout, stderr string)
	}{
		{
			name: "cat",
			setup: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			script: "cat hello.txt",
			assert: func(t *testing.T, dir string, fsys *trackingFS, stdout, stderr string) {
				if stdout != "hello" {
					t.Fatalf("stdout = %q, want %q", stdout, "hello")
				}
				if fsys.openCount == 0 {
					t.Fatalf("expected Open to be used")
				}
			},
		},
		{
			name: "base64",
			setup: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			script: "base64 hello.txt",
			assert: func(t *testing.T, dir string, fsys *trackingFS, stdout, stderr string) {
				if fsys.openCount == 0 {
					t.Fatalf("expected Open to be used")
				}
				if strings.TrimSpace(stdout) == "" {
					t.Fatalf("expected encoded output")
				}
			},
		},
		{
			name: "shasum",
			setup: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			script: "shasum hello.txt",
			assert: func(t *testing.T, dir string, fsys *trackingFS, stdout, stderr string) {
				if fsys.openCount == 0 {
					t.Fatalf("expected Open to be used")
				}
				if !strings.Contains(stdout, "hello.txt") {
					t.Fatalf("expected shasum output, got %q", stdout)
				}
			},
		},
		{
			name: "head",
			setup: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "lines.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			script: "head -n 2 lines.txt",
			assert: func(t *testing.T, dir string, fsys *trackingFS, stdout, stderr string) {
				if stdout != "one\ntwo\n" {
					t.Fatalf("stdout = %q, want %q", stdout, "one\ntwo\n")
				}
				if fsys.openCount == 0 {
					t.Fatalf("expected Open to be used")
				}
			},
		},
		{
			name: "grep",
			setup: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "lines.txt"), []byte("alpha\nbeta\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			script: "grep -F -n et lines.txt",
			assert: func(t *testing.T, dir string, fsys *trackingFS, stdout, stderr string) {
				if stdout != "2:beta\n" {
					t.Fatalf("stdout = %q, want %q", stdout, "2:beta\n")
				}
				if fsys.openCount == 0 {
					t.Fatalf("expected Open to be used")
				}
			},
		},
		{
			name: "sed",
			setup: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "lines.txt"), []byte("foo\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			script: "sed 's/o/a/g' lines.txt",
			assert: func(t *testing.T, dir string, fsys *trackingFS, stdout, stderr string) {
				if stdout != "faa\n" {
					t.Fatalf("stdout = %q, want %q", stdout, "faa\n")
				}
				if fsys.openCount == 0 {
					t.Fatalf("expected Open to be used")
				}
			},
		},
		{
			name: "sort",
			setup: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "lines.txt"), []byte("beta\nalpha\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			script: "sort lines.txt",
			assert: func(t *testing.T, dir string, fsys *trackingFS, stdout, stderr string) {
				if stdout != "alpha\nbeta\n" {
					t.Fatalf("stdout = %q, want %q", stdout, "alpha\nbeta\n")
				}
				if fsys.openCount == 0 {
					t.Fatalf("expected Open to be used")
				}
			},
		},
		{
			name: "cut",
			setup: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "fields.txt"), []byte("left,right,center\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			script: "cut -d, -f2 fields.txt",
			assert: func(t *testing.T, dir string, fsys *trackingFS, stdout, stderr string) {
				if stdout != "right\n" {
					t.Fatalf("stdout = %q, want %q", stdout, "right\n")
				}
				if fsys.openCount == 0 {
					t.Fatalf("expected Open to be used")
				}
			},
		},
		{
			name: "uniq",
			setup: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "lines.txt"), []byte("alpha\nalpha\nbeta\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			script: "uniq -c lines.txt",
			assert: func(t *testing.T, dir string, fsys *trackingFS, stdout, stderr string) {
				if stdout != "2 alpha\n1 beta\n" {
					t.Fatalf("stdout = %q, want %q", stdout, "2 alpha\n1 beta\n")
				}
				if fsys.openCount == 0 {
					t.Fatalf("expected Open to be used")
				}
			},
		},
		{
			name: "tail",
			setup: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "lines.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			script: "tail -n 1 lines.txt",
			assert: func(t *testing.T, dir string, fsys *trackingFS, stdout, stderr string) {
				if stdout != "three\n" {
					t.Fatalf("stdout = %q, want %q", stdout, "three\n")
				}
				if fsys.openCount == 0 {
					t.Fatalf("expected Open to be used")
				}
			},
		},
		{
			name: "wc",
			setup: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "lines.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			script: "wc -l lines.txt",
			assert: func(t *testing.T, dir string, fsys *trackingFS, stdout, stderr string) {
				if stdout != "3 lines.txt\n" {
					t.Fatalf("stdout = %q, want %q", stdout, "3 lines.txt\n")
				}
				if fsys.openCount == 0 {
					t.Fatalf("expected Open to be used")
				}
			},
		},
		{
			name: "jq",
			setup: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "data.json"), []byte("{\"foo\":\"bar\"}\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			script: "jq -r .foo data.json",
			assert: func(t *testing.T, dir string, fsys *trackingFS, stdout, stderr string) {
				if stdout != "bar\n" {
					t.Fatalf("stdout = %q, want %q", stdout, "bar\n")
				}
				if fsys.openCount == 0 {
					t.Fatalf("expected Open to be used")
				}
			},
		},
		{
			name: "awk",
			setup: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "program.awk"), []byte("{ print $1 }"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			stdin:  "left right\n",
			script: "awk -f program.awk",
			assert: func(t *testing.T, dir string, fsys *trackingFS, stdout, stderr string) {
				if stdout != "left\n" {
					t.Fatalf("stdout = %q, want %q", stdout, "left\n")
				}
				if fsys.openCount == 0 {
					t.Fatalf("expected Open to be used")
				}
			},
		},
		{
			name:   "touch",
			script: "touch touched.txt",
			assert: func(t *testing.T, dir string, fsys *trackingFS, stdout, stderr string) {
				if _, err := os.Stat(filepath.Join(dir, "touched.txt")); err != nil {
					t.Fatalf("expected touched file: %v", err)
				}
				if fsys.openFileCount == 0 || fsys.chtimesCount == 0 {
					t.Fatalf("expected OpenFile and Chtimes to be used")
				}
			},
		},
		{
			name:   "mkdir",
			script: "mkdir created",
			assert: func(t *testing.T, dir string, fsys *trackingFS, stdout, stderr string) {
				if stat, err := os.Stat(filepath.Join(dir, "created")); err != nil || !stat.IsDir() {
					t.Fatalf("expected created directory, got err=%v", err)
				}
				if fsys.mkdirCount == 0 {
					t.Fatalf("expected Mkdir to be used")
				}
			},
		},
		{
			name: "rm",
			setup: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "remove-me.txt"), []byte("bye"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			script: "rm remove-me.txt",
			assert: func(t *testing.T, dir string, fsys *trackingFS, stdout, stderr string) {
				if _, err := os.Stat(filepath.Join(dir, "remove-me.txt")); !os.IsNotExist(err) {
					t.Fatalf("expected file to be removed")
				}
				if fsys.removeCount == 0 {
					t.Fatalf("expected Remove to be used")
				}
			},
		},
		{
			name: "ls",
			setup: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			script: "ls",
			assert: func(t *testing.T, dir string, fsys *trackingFS, stdout, stderr string) {
				if !strings.Contains(stdout, "hello.txt") {
					t.Fatalf("expected ls output, got %q", stdout)
				}
				if fsys.readDirCount == 0 || fsys.lstatCount == 0 {
					t.Fatalf("expected ReadDir and Lstat to be used")
				}
			},
		},
		{
			name: "find",
			setup: func(t *testing.T, dir string) {
				if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "subdir", "hello.txt"), []byte("hello"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			script: "find .",
			assert: func(t *testing.T, dir string, fsys *trackingFS, stdout, stderr string) {
				if !strings.Contains(stdout, "subdir/hello.txt") {
					t.Fatalf("expected find output, got %q", stdout)
				}
				if fsys.readDirCount == 0 || fsys.lstatCount == 0 {
					t.Fatalf("expected ReadDir and Lstat to be used")
				}
			},
		},
		{
			name: "chmod",
			setup: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "mode.txt"), []byte("hello"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			script: "chmod 755 mode.txt",
			assert: func(t *testing.T, dir string, fsys *trackingFS, stdout, stderr string) {
				info, err := os.Stat(filepath.Join(dir, "mode.txt"))
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != 0o755 {
					t.Fatalf("mode = %#o, want %#o", info.Mode().Perm(), 0o755)
				}
				if fsys.chmodCount == 0 {
					t.Fatalf("expected Chmod to be used")
				}
			},
		},
		{
			name: "cp",
			setup: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "source.txt"), []byte("hello"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			script: "cp source.txt copied.txt",
			assert: func(t *testing.T, dir string, fsys *trackingFS, stdout, stderr string) {
				content, err := os.ReadFile(filepath.Join(dir, "copied.txt"))
				if err != nil {
					t.Fatal(err)
				}
				if string(content) != "hello" {
					t.Fatalf("copied content = %q", string(content))
				}
				if fsys.openCount == 0 || fsys.openFileCount == 0 {
					t.Fatalf("expected Open and OpenFile to be used")
				}
			},
		},
		{
			name: "mv",
			setup: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "move.txt"), []byte("hello"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			script: "mv move.txt moved.txt",
			assert: func(t *testing.T, dir string, fsys *trackingFS, stdout, stderr string) {
				if _, err := os.Stat(filepath.Join(dir, "moved.txt")); err != nil {
					t.Fatalf("expected moved file: %v", err)
				}
				if _, err := os.Stat(filepath.Join(dir, "move.txt")); !os.IsNotExist(err) {
					t.Fatalf("expected source to be gone")
				}
				if fsys.renameCount == 0 {
					t.Fatalf("expected Rename to be used")
				}
			},
		},
		{
			name:   "mktemp",
			script: "mktemp -p .",
			assert: func(t *testing.T, dir string, fsys *trackingFS, stdout, stderr string) {
				created := strings.TrimSpace(stdout)
				if created == "" {
					t.Fatalf("expected mktemp output")
				}
				if _, err := os.Stat(created); err != nil {
					t.Fatalf("expected temp file to exist: %v", err)
				}
				if fsys.openFileCount == 0 {
					t.Fatalf("expected OpenFile to be used")
				}
			},
		},
		{
			name:   "mktemp bsd compat",
			script: "mktemp -t report",
			assert: func(t *testing.T, dir string, fsys *trackingFS, stdout, stderr string) {
				created := strings.TrimSpace(stdout)
				if created == "" {
					t.Fatalf("expected mktemp output")
				}
				if !strings.Contains(filepath.Base(created), "report") {
					t.Fatalf("created path = %q, want prefix %q", created, "report")
				}
				if _, err := os.Stat(created); err != nil {
					t.Fatalf("expected temp file to exist: %v", err)
				}
				if fsys.openFileCount == 0 {
					t.Fatalf("expected OpenFile to be used")
				}
			},
		},
		{
			name: "gzip",
			setup: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "compress.txt"), []byte("hello"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			script: "gzip compress.txt",
			assert: func(t *testing.T, dir string, fsys *trackingFS, stdout, stderr string) {
				if _, err := os.Stat(filepath.Join(dir, "compress.txt.gz")); err != nil {
					t.Fatalf("expected compressed file: %v", err)
				}
				if _, err := os.Stat(filepath.Join(dir, "compress.txt")); !os.IsNotExist(err) {
					t.Fatalf("expected source file to be removed")
				}
				if fsys.openCount == 0 || fsys.openFileCount == 0 || fsys.removeCount == 0 {
					t.Fatalf("expected gzip to use adapter open/openfile/remove")
				}
			},
		},
		{
			name: "tar",
			setup: func(t *testing.T, dir string) {
				if err := os.Mkdir(filepath.Join(dir, "bundle"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "bundle", "hello.txt"), []byte("hello"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			script: "tar -cf archive.tar bundle",
			assert: func(t *testing.T, dir string, fsys *trackingFS, stdout, stderr string) {
				if _, err := os.Stat(filepath.Join(dir, "archive.tar")); err != nil {
					t.Fatalf("expected archive: %v", err)
				}
				if fsys.openCount == 0 || fsys.openFileCount == 0 || fsys.readDirCount == 0 {
					t.Fatalf("expected tar to use adapter open/openfile/readdir")
				}
			},
		},
		{
			name: "zip",
			setup: func(t *testing.T, dir string) {
				if err := os.Mkdir(filepath.Join(dir, "bundle"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "bundle", "hello.txt"), []byte("hello"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			script: "zip -r archive.zip bundle",
			assert: func(t *testing.T, dir string, fsys *trackingFS, stdout, stderr string) {
				if _, err := os.Stat(filepath.Join(dir, "archive.zip")); err != nil {
					t.Fatalf("expected archive: %v", err)
				}
				if fsys.openCount == 0 || fsys.openFileCount == 0 || fsys.readDirCount == 0 {
					t.Fatalf("expected zip to use adapter open/openfile/readdir")
				}
			},
		},
		{
			name: "unzip",
			setup: func(t *testing.T, dir string) {
				archivePath := filepath.Join(dir, "archive.zip")
				file, err := os.Create(archivePath)
				if err != nil {
					t.Fatal(err)
				}
				writer := archivezip.NewWriter(file)
				entry, err := writer.Create("bundle/hello.txt")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := io.WriteString(entry, "hello"); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			},
			script: "unzip -d out archive.zip",
			assert: func(t *testing.T, dir string, fsys *trackingFS, stdout, stderr string) {
				content, err := os.ReadFile(filepath.Join(dir, "out", "bundle", "hello.txt"))
				if err != nil {
					t.Fatalf("expected extracted file: %v", err)
				}
				if string(content) != "hello" {
					t.Fatalf("content = %q, want %q", string(content), "hello")
				}
				if fsys.openCount == 0 || fsys.openFileCount == 0 || fsys.mkdirAllCount == 0 {
					t.Fatalf("expected unzip to use adapter open/openfile/mkdirall")
				}
			},
		},
		{
			name:   "xargs",
			stdin:  "hello\n",
			script: "xargs echo",
			assert: func(t *testing.T, dir string, fsys *trackingFS, stdout, stderr string) {
				if stdout != "hello\n" {
					t.Fatalf("stdout = %q, want %q", stdout, "hello\n")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.setup != nil {
				tt.setup(t, dir)
			}
			fsys := &trackingFS{}
			stdout, stderr, err := runCoreutilsScriptWithOptions(t, fsys, dir, tt.stdin, tt.script, tt.opts...)
			if err != nil {
				t.Fatalf("run error: %v\nstderr=%s", err, stderr)
			}
			tt.assert(t, dir, fsys, stdout, stderr)
		})
	}
}

func TestFindSupportsPosixOperandOrder(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "subdir", "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, dir, "", "find . -type f")
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "subdir/hello.txt") {
		t.Fatalf("stdout = %q, want file path", stdout)
	}
}

func TestFindKeepsLegacyOptionsBeforePath(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "subdir", "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, dir, "", "find -type f .")
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "subdir/hello.txt") {
		t.Fatalf("stdout = %q, want file path", stdout)
	}
}

func TestFindSupportsMaxDepth(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "subdir", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "subdir", "nested", "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, dir, "", "find . -maxdepth 1")
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if strings.Contains(stdout, "subdir/nested") || strings.Contains(stdout, "subdir/nested/hello.txt") {
		t.Fatalf("stdout = %q, want traversal to stop at depth 1", stdout)
	}
	if !strings.Contains(stdout, "subdir") {
		t.Fatalf("stdout = %q, want top-level directory entry", stdout)
	}
}

func TestFindSupportsPathPredicate(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "subdir", "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "root.txt"), []byte("root"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, dir, "", "find . -path './subdir/*'")
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if strings.Contains(stdout, "root.txt") {
		t.Fatalf("stdout = %q, want root.txt to be filtered out", stdout)
	}
	if !strings.Contains(stdout, "subdir/hello.txt") {
		t.Fatalf("stdout = %q, want nested file path", stdout)
	}
}

func TestFindSupportsOrExpressionsAndCaseInsensitiveName(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pkg", "agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "OPENAI.md"), []byte("openai"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runCoreutilsScript(
		t,
		interp.OSFileSystem{},
		dir,
		"",
		"find . -maxdepth 3 \\( -iname '*openai*' -o -path '*/agent*' \\) -print",
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "docs/OPENAI.md") {
		t.Fatalf("stdout = %q, want case-insensitive name match", stdout)
	}
	if !strings.Contains(stdout, "pkg/agent") {
		t.Fatalf("stdout = %q, want OR path match", stdout)
	}
	if strings.Contains(stdout, "notes.txt") {
		t.Fatalf("stdout = %q, did not expect unrelated file", stdout)
	}
}

func TestChmodSupportsSymbolicCopyModes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mode.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o640); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, dir, "", "chmod g=u mode.txt")
	if err != nil {
		t.Fatalf("run error: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o660 {
		t.Fatalf("mode = %#o, want %#o", got, 0o660)
	}
}

func TestTouchSupportsPosixTimestampOption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stamp.txt")

	stdout, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, dir, "", "touch -t 202401020304.05 stamp.txt")
	if err != nil {
		t.Fatalf("run error: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2024, time.January, 2, 3, 4, 5, 0, time.Local)
	if !info.ModTime().Equal(want) {
		t.Fatalf("mtime = %v, want %v", info.ModTime(), want)
	}
}

func TestMkdirSupportsSymbolicModes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "created")

	stdout, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, dir, "", "mkdir -m u=rwx,go=rx created")
	if err != nil {
		t.Fatalf("run error: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("mode = %#o, want %#o", got, 0o755)
	}
}

func TestEnvAppliesAssignmentsToDispatchedCommands(t *testing.T) {
	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		"",
		"env BASE=outer CHILD=value env",
		interp.Env(expand.ListEnviron("BASE=inner")),
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "BASE=outer\n") {
		t.Fatalf("stdout = %q, want overridden BASE", stdout)
	}
	if !strings.Contains(stdout, "CHILD=value\n") {
		t.Fatalf("stdout = %q, want CHILD assignment", stdout)
	}
}

func TestEnvSupportsIgnoreEnvironment(t *testing.T) {
	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		"",
		"env -i ONLY=value env",
		interp.Env(expand.ListEnviron("BASE=present")),
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if strings.Contains(stdout, "BASE=present\n") {
		t.Fatalf("stdout = %q, did not expect inherited env", stdout)
	}
	if !strings.Contains(stdout, "ONLY=value\n") {
		t.Fatalf("stdout = %q, want ONLY assignment", stdout)
	}
}

func TestGrepSupportsQuietExitStatus(t *testing.T) {
	stdout, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, t.TempDir(), "alpha\nbeta\n", "grep -F -q beta")
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}

	_, stderr, err = runCoreutilsScript(t, interp.OSFileSystem{}, t.TempDir(), "alpha\nbeta\n", "grep -F -q gamma")
	if !errors.Is(err, interp.ExitStatus(1)) {
		t.Fatalf("error = %v, want exit status 1\nstderr=%s", err, stderr)
	}
}

func TestGrepPrefixesFileNamesForMultipleInputs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "one.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "two.txt"), []byte("beta\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, dir, "", "grep -F a one.txt two.txt")
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "one.txt:alpha\ntwo.txt:beta\n" {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestGrepSupportsRecursiveSearch(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pkg", "agent", "openai"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pkg", "agent", "openai", "driver.go"), []byte("package agent\n// openai driver\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("unrelated\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, dir, "", "grep -R -n -E 'openai|package agent' .")
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "pkg/agent/openai/driver.go:1:package agent") {
		t.Fatalf("stdout = %q, want recursive package match", stdout)
	}
	if !strings.Contains(stdout, "pkg/agent/openai/driver.go:2:// openai driver") {
		t.Fatalf("stdout = %q, want recursive openai match", stdout)
	}
}

func TestGrepSupportsOnlyMatchingExtendedAlternation(t *testing.T) {
	stdout, stderr, err := runCoreutilsScript(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		`<a href="/one">First</a> <span>Snippet</span> <a href="/two">Second</a>`+"\n",
		`grep -E -o '<a[^>]*>[^<]*</a>|<span>[^<]*</span>'`,
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	want := "<a href=\"/one\">First</a>\n<span>Snippet</span>\n<a href=\"/two\">Second</a>\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
}

func TestGrepRejectsMissingExplicitMatcherMode(t *testing.T) {
	_, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, t.TempDir(), "", `grep beta`)
	if err == nil {
		t.Fatalf("expected grep to fail\nstderr=%s", stderr)
	}
	if !errors.Is(err, interp.ExitStatus(2)) {
		t.Fatalf("error = %v, want exit status 2\nstderr=%s", err, stderr)
	}
	if !strings.Contains(stderr, "grep: use -F for literal matches or -E for regex patterns; plain grep without -E/-F is not allowed") {
		t.Fatalf("stderr = %q, want explicit matcher mode rejection", stderr)
	}
	if !strings.Contains(stderr, "usage: grep [-E|-F] [-R|-r] [-ivncloq] [-e pattern]... [pattern] [file ...]") {
		t.Fatalf("stderr = %q, want grep usage", stderr)
	}
}

func TestGrepExtendedCompileErrorsRemainSupported(t *testing.T) {
	_, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, t.TempDir(), "", `grep -E '['`)
	if err == nil {
		t.Fatalf("expected grep -E to fail\nstderr=%s", stderr)
	}
	if !strings.Contains(err.Error(), "error parsing regexp") {
		t.Fatalf("error = %v, want regexp compile failure\nstderr=%s", err, stderr)
	}
	if strings.Contains(stderr, "plain grep without -E/-F is not allowed") {
		t.Fatalf("stderr = %q, did not expect missing-mode rejection", stderr)
	}
}

func TestHeadSupportsLegacyNumericCountSyntax(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lines.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, dir, "", "head -2 lines.txt")
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "one\ntwo\n" {
		t.Fatalf("stdout = %q, want %q", stdout, "one\ntwo\n")
	}
}

func TestTailSupportsLegacyNumericCountSyntax(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lines.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, dir, "", "tail -2 lines.txt")
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "two\nthree\n" {
		t.Fatalf("stdout = %q, want %q", stdout, "two\nthree\n")
	}
}

func TestSedSupportsCustomDelimiterAndMultipleScripts(t *testing.T) {
	stdout, stderr, err := runCoreutilsScript(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		"C:\\temp\n",
		"sed -e 's@\\\\@/@g' -e 's/temp/files/g'",
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "C:/files\n" {
		t.Fatalf("stdout = %q, want %q", stdout, "C:/files\n")
	}
}

func TestUnamePrintsSelectedFields(t *testing.T) {
	stdout, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, t.TempDir(), "", "uname -sm")
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	fields := strings.Fields(strings.TrimSpace(stdout))
	if len(fields) != 2 {
		t.Fatalf("stdout = %q, want two fields", stdout)
	}
}

func TestUnzipSkipsEscapingPaths(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "archive.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := archivezip.NewWriter(file)
	entry, err := writer.Create("../escape.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(entry, "escape"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, dir, "", "unzip -d out archive.zip")
	if err != nil {
		t.Fatalf("run error: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected escape path to be skipped, err=%v", err)
	}
	if !strings.Contains(stderr, "Skipping file") {
		t.Fatalf("stderr = %q, want skip warning", stderr)
	}
}

func TestXargsPreservesQuotedArguments(t *testing.T) {
	stdout, stderr, err := runCoreutilsScript(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		`one 'two three' ""`,
		"xargs -n 1 printf '<%s>\\n'",
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "<one>\n<two three>\n<>\n" {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestXargsRunsDefaultCommandWithoutInput(t *testing.T) {
	stdout, stderr, err := runCoreutilsScript(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		"",
		"xargs printf 'ok\\n'",
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "ok\n" {
		t.Fatalf("stdout = %q, want %q", stdout, "ok\n")
	}
}

func TestCurlRejectsNetworkByDefault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello")
	}))
	defer server.Close()

	stdout, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, t.TempDir(), "", "curl "+server.URL)
	if err == nil {
		t.Fatalf("expected curl to fail without a dialer override")
	}
	if !errors.Is(err, interp.ErrNetworkDialRejected) {
		t.Fatalf("error = %v, want %v", err, interp.ErrNetworkDialRejected)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "network dialing disabled") {
		t.Fatalf("stderr = %q, want network failure message", stderr)
	}
}

func TestCurlRejectsDisallowedHostViaAllowlistDialer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello")
	}))
	defer server.Close()

	dialer, err := interp.NewAllowlistNetworkDialer(interp.OSNetworkDialer{}, []interp.HostMatcher{{
		Glob: "example.com",
	}})
	if err != nil {
		t.Fatalf("NewAllowlistNetworkDialer() error = %v", err)
	}

	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		"",
		"curl "+server.URL,
		interp.Network(dialer),
	)
	if err == nil {
		t.Fatal("expected curl to fail for disallowed host")
	}
	if !errors.Is(err, interp.ErrNetworkHostRejected) {
		t.Fatalf("error = %v, want %v", err, interp.ErrNetworkHostRejected)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "network host not allowed") {
		t.Fatalf("stderr = %q, want allowlist failure message", stderr)
	}
}

func TestCurlUsesInjectedDialerAndFilesystemAdapter(t *testing.T) {
	var (
		gotMethod string
		gotHeader string
		gotBody   string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotHeader = r.Header.Get("X-Test")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll error: %v", err)
		}
		gotBody = string(body)
		w.Header().Set("X-Reply", "ok")
		fmt.Fprint(w, "response-body")
	}))
	defer server.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "payload.txt"), []byte("hello=world"), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	fsys := &trackingFS{}
	script := "curl -H 'X-Test: yes' -d @payload.txt -o response.txt " + server.URL
	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		fsys,
		dir,
		"",
		script,
		interp.Network(interp.OSNetworkDialer{}),
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want %q", gotMethod, http.MethodPost)
	}
	if gotHeader != "yes" {
		t.Fatalf("header = %q, want %q", gotHeader, "yes")
	}
	if gotBody != "hello=world" {
		t.Fatalf("body = %q, want %q", gotBody, "hello=world")
	}
	content, err := os.ReadFile(filepath.Join(dir, "response.txt"))
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if string(content) != "response-body" {
		t.Fatalf("response file = %q, want %q", string(content), "response-body")
	}
	if fsys.openCount == 0 || fsys.openFileCount == 0 {
		t.Fatalf("expected curl to use filesystem adapter for request body and output file")
	}
}

func TestCurlSupportsDumpHeaderFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Reply", "ok")
		fmt.Fprint(w, "response-body")
	}))
	defer server.Close()

	dir := t.TempDir()
	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		dir,
		"",
		"curl -D headers.txt -o response.txt "+server.URL,
		interp.Network(interp.OSNetworkDialer{}),
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}

	responseBody, err := os.ReadFile(filepath.Join(dir, "response.txt"))
	if err != nil {
		t.Fatalf("ReadFile response error: %v", err)
	}
	if string(responseBody) != "response-body" {
		t.Fatalf("response file = %q, want %q", string(responseBody), "response-body")
	}

	headers, err := os.ReadFile(filepath.Join(dir, "headers.txt"))
	if err != nil {
		t.Fatalf("ReadFile headers error: %v", err)
	}
	headerText := string(headers)
	if !strings.Contains(headerText, "HTTP/1.1 200 OK\r\n") {
		t.Fatalf("headers = %q, want HTTP status line", headerText)
	}
	if !strings.Contains(headerText, "X-Reply: ok\r\n") {
		t.Fatalf("headers = %q, want dumped response header", headerText)
	}
}

func TestCurlSupportsDumpHeaderToStdout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Reply", "ok")
		fmt.Fprint(w, "response-body")
	}))
	defer server.Close()

	dir := t.TempDir()
	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		dir,
		"",
		"curl --dump-header - -o response.txt "+server.URL,
		interp.Network(interp.OSNetworkDialer{}),
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "HTTP/1.1 200 OK\r\n") {
		t.Fatalf("stdout = %q, want HTTP status line", stdout)
	}
	if !strings.Contains(stdout, "X-Reply: ok\r\n") {
		t.Fatalf("stdout = %q, want dumped response header", stdout)
	}
	if strings.Contains(stdout, "response-body") {
		t.Fatalf("stdout = %q, want headers only", stdout)
	}

	responseBody, err := os.ReadFile(filepath.Join(dir, "response.txt"))
	if err != nil {
		t.Fatalf("ReadFile response error: %v", err)
	}
	if string(responseBody) != "response-body" {
		t.Fatalf("response file = %q, want %q", string(responseBody), "response-body")
	}
}

func TestCurlSupportsDataBinaryLongOption(t *testing.T) {
	var (
		gotMethod string
		gotBody   []byte
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll error: %v", err)
		}
		gotBody = body
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	dir := t.TempDir()
	payload := []byte("{\"hello\":\"world\"}\n")
	if err := os.WriteFile(filepath.Join(dir, "payload.json"), payload, 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		dir,
		"",
		"curl --data-binary @payload.json "+server.URL,
		interp.Network(interp.OSNetworkDialer{}),
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "ok" {
		t.Fatalf("stdout = %q, want %q", stdout, "ok")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want %q", gotMethod, http.MethodPost)
	}
	if !bytes.Equal(gotBody, payload) {
		t.Fatalf("body = %q, want %q", string(gotBody), string(payload))
	}
}

func TestCurlDataStripsCarriageReturnsNewlinesAndNullBytesFromFiles(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll error: %v", err)
		}
		gotBody = string(body)
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	dir := t.TempDir()
	payload := []byte("a\r\nb\x00c\n")
	if err := os.WriteFile(filepath.Join(dir, "payload.txt"), payload, 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		dir,
		"",
		"curl -sS -d @payload.txt "+server.URL,
		interp.Network(interp.OSNetworkDialer{}),
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "ok" {
		t.Fatalf("stdout = %q, want %q", stdout, "ok")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if gotBody != "abc" {
		t.Fatalf("body = %q, want %q", gotBody, "abc")
	}
}

func TestCurlSupportsDataAsciiAlias(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll error: %v", err)
		}
		gotBody = string(body)
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "payload.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		dir,
		"",
		"curl -sS --data-ascii @payload.txt "+server.URL,
		interp.Network(interp.OSNetworkDialer{}),
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "ok" {
		t.Fatalf("stdout = %q, want %q", stdout, "ok")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if gotBody != "onetwo" {
		t.Fatalf("body = %q, want %q", gotBody, "onetwo")
	}
}

func TestCurlInjectsConfiguredHeadersForMatchingDomains(t *testing.T) {
	var (
		gotAuthorization string
		gotScoped        string
		gotHost          string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		gotScoped = r.Header.Get("X-Scoped")
		gotHost = r.Host
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	dialer := &hostMapDialer{
		hostToAddress: map[string]string{
			"payments-abc.example.com": server.Listener.Addr().String(),
		},
	}
	httpClientFactory := mustHTTPClientFactory(t, dialer, []interp.HTTPHeaderRule{
		{
			Name:  "Authorization",
			Value: "Bearer secret",
			Domains: []interp.HTTPDomainMatcher{{
				Glob: "*.example.com",
			}},
		},
		{
			Name:  "X-Scoped",
			Value: "regex-match",
			Domains: []interp.HTTPDomainMatcher{{
				Regex: "^payments-[a-z0-9-]+\\.example\\.com$",
			}},
		},
	})

	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		"",
		"curl -sS http://payments-abc.example.com",
		interp.Network(dialer),
		interp.HTTPClient(httpClientFactory),
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "ok" {
		t.Fatalf("stdout = %q, want %q", stdout, "ok")
	}
	if gotAuthorization != "Bearer secret" {
		t.Fatalf("authorization = %q, want %q", gotAuthorization, "Bearer secret")
	}
	if gotScoped != "regex-match" {
		t.Fatalf("X-Scoped = %q, want %q", gotScoped, "regex-match")
	}
	if gotHost != "payments-abc.example.com" {
		t.Fatalf("host = %q, want %q", gotHost, "payments-abc.example.com")
	}
}

func TestCurlConfiguredHeadersOverrideUserHeaders(t *testing.T) {
	var gotAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	dialer := &hostMapDialer{
		hostToAddress: map[string]string{
			"api.example.com": server.Listener.Addr().String(),
		},
	}
	httpClientFactory := mustHTTPClientFactory(t, dialer, []interp.HTTPHeaderRule{{
		Name:  "Authorization",
		Value: "Bearer platform",
		Domains: []interp.HTTPDomainMatcher{{
			Glob: "api.example.com",
		}},
	}})

	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		"",
		"curl -sS -H 'Authorization: Bearer user' http://api.example.com",
		interp.Network(dialer),
		interp.HTTPClient(httpClientFactory),
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "ok" {
		t.Fatalf("stdout = %q, want %q", stdout, "ok")
	}
	if gotAuthorization != "Bearer platform" {
		t.Fatalf("authorization = %q, want %q", gotAuthorization, "Bearer platform")
	}
}

func TestCurlHeaderTraceDoesNotPolluteShellStderr(t *testing.T) {
	t.Setenv("CURL_DEBUG_HEADERS", "1")

	var gotAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	dialer := &hostMapDialer{
		hostToAddress: map[string]string{
			"api.example.com": server.Listener.Addr().String(),
		},
	}
	httpClientFactory := mustHTTPClientFactory(t, dialer, []interp.HTTPHeaderRule{{
		Name:  "Authorization",
		Value: "Bearer platform",
		Domains: []interp.HTTPDomainMatcher{{
			Glob: "api.example.com",
		}},
	}})

	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		"",
		"curl -sS http://api.example.com",
		interp.Network(dialer),
		interp.HTTPClient(httpClientFactory),
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "ok" {
		t.Fatalf("stdout = %q, want %q", stdout, "ok")
	}
	if gotAuthorization != "Bearer platform" {
		t.Fatalf("authorization = %q, want %q", gotAuthorization, "Bearer platform")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty shell stderr", stderr)
	}
}

func TestCurlReappliesConfiguredHeadersAcrossRedirects(t *testing.T) {
	var (
		startAuthorization string
		finalAuthorization string
	)
	finalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		finalAuthorization = r.Header.Get("Authorization")
		fmt.Fprint(w, "final")
	}))
	defer finalServer.Close()

	redirectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startAuthorization = r.Header.Get("Authorization")
		http.Redirect(w, r, "http://api.example.com/final", http.StatusFound)
	}))
	defer redirectServer.Close()

	dialer := &hostMapDialer{
		hostToAddress: map[string]string{
			"start.example.com": redirectServer.Listener.Addr().String(),
			"api.example.com":   finalServer.Listener.Addr().String(),
		},
	}
	httpClientFactory := mustHTTPClientFactory(t, dialer, []interp.HTTPHeaderRule{{
		Name:  "Authorization",
		Value: "Bearer redirect-secret",
		Domains: []interp.HTTPDomainMatcher{{
			Glob: "api.example.com",
		}},
	}})

	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		"",
		"curl -sS -L http://start.example.com/start",
		interp.Network(dialer),
		interp.HTTPClient(httpClientFactory),
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "final" {
		t.Fatalf("stdout = %q, want %q", stdout, "final")
	}
	if startAuthorization != "" {
		t.Fatalf("start authorization = %q, want empty for non-matching redirect source", startAuthorization)
	}
	if finalAuthorization != "Bearer redirect-secret" {
		t.Fatalf("final authorization = %q, want %q", finalAuthorization, "Bearer redirect-secret")
	}
}

func TestCurlSupportsUserAgentShorthand(t *testing.T) {
	var gotUserAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUserAgent = r.Header.Get("User-Agent")
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		"",
		"curl -A 'Mozilla/5.0' "+server.URL,
		interp.Network(interp.OSNetworkDialer{}),
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "ok" {
		t.Fatalf("stdout = %q, want %q", stdout, "ok")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if gotUserAgent != "Mozilla/5.0" {
		t.Fatalf("user-agent = %q, want %q", gotUserAgent, "Mozilla/5.0")
	}
}

func TestCurlSupportsShowErrorCompatibility(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		"",
		"curl -sS "+server.URL,
		interp.Network(interp.OSNetworkDialer{}),
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "ok" {
		t.Fatalf("stdout = %q, want %q", stdout, "ok")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
}

func TestCurlSilentShowErrorPrintsHTTPFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer server.Close()

	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		"",
		"curl -sSf "+server.URL,
		interp.Network(interp.OSNetworkDialer{}),
	)
	if err == nil {
		t.Fatalf("expected curl to fail")
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if stderr != "curl: server returned HTTP 502\n" {
		t.Fatalf("stderr = %q, want HTTP failure message", stderr)
	}
	if !strings.Contains(err.Error(), "curl: server returned HTTP 502") {
		t.Fatalf("error = %v, want HTTP failure", err)
	}
	if !errors.Is(err, interp.ExitStatus(22)) {
		t.Fatalf("error = %v, want exit status 22", err)
	}
}

func TestCurlFailPrintsErrorAndUsesExitStatus22(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer server.Close()

	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		"",
		"curl -f "+server.URL,
		interp.Network(interp.OSNetworkDialer{}),
	)
	if !errors.Is(err, interp.ExitStatus(22)) {
		t.Fatalf("error = %v, want exit status 22", err)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if stderr != "curl: server returned HTTP 404\n" {
		t.Fatalf("stderr = %q, want HTTP failure message", stderr)
	}
}

func TestCurlFailStillIncludesHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Reply", "missing")
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer server.Close()

	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		"",
		"curl -f -i "+server.URL,
		interp.Network(interp.OSNetworkDialer{}),
	)
	if !errors.Is(err, interp.ExitStatus(22)) {
		t.Fatalf("error = %v, want exit status 22", err)
	}
	if !strings.Contains(stdout, "HTTP/1.1 404 Not Found\r\n") {
		t.Fatalf("stdout = %q, want HTTP status line", stdout)
	}
	if !strings.Contains(stdout, "X-Reply: missing\r\n") {
		t.Fatalf("stdout = %q, want response header", stdout)
	}
	if strings.Contains(stdout, "nope") {
		t.Fatalf("stdout = %q, want headers only", stdout)
	}
	if stderr != "curl: server returned HTTP 404\n" {
		t.Fatalf("stderr = %q, want HTTP failure message", stderr)
	}
}

func TestCurlWriteOutSupportsHTTPCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		"",
		"curl -sS -L -o /dev/null -w '%{http_code}' "+server.URL,
		interp.Network(interp.OSNetworkDialer{}),
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "204" {
		t.Fatalf("stdout = %q, want %q", stdout, "204")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
}

func TestCurlWriteOutRunsOnHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, `{"error":"bad gateway"}`)
	}))
	defer server.Close()

	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		"",
		"curl -sSf -o /dev/null -w '%{exitcode}|%{errormsg}|%{http_code}|%{content_type}' "+server.URL,
		interp.Network(interp.OSNetworkDialer{}),
	)
	if !errors.Is(err, interp.ExitStatus(22)) {
		t.Fatalf("error = %v, want exit status 22", err)
	}
	if stdout != "22|curl: server returned HTTP 502|502|application/json" {
		t.Fatalf("stdout = %q, want failure write-out", stdout)
	}
	if stderr != "curl: server returned HTTP 502\n" {
		t.Fatalf("stderr = %q, want HTTP failure message", stderr)
	}
}

func TestCurlWriteOutSupportsNumRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/middle", http.StatusFound)
		case "/middle":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			fmt.Fprint(w, "ok")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		"",
		"curl -sS -L -o /dev/null -w '%{num_redirects}|%{url_effective}' "+server.URL+"/start",
		interp.Network(interp.OSNetworkDialer{}),
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "2|"+server.URL+"/final" {
		t.Fatalf("stdout = %q, want redirect write-out", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
}

func TestCurlRedirectsImplicitPostToGet(t *testing.T) {
	var (
		startMethod string
		startBody   string
		finalMethod string
		finalBody   string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll error: %v", err)
		}
		switch r.URL.Path {
		case "/start":
			startMethod = r.Method
			startBody = string(body)
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			finalMethod = r.Method
			finalBody = string(body)
			fmt.Fprint(w, "ok")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		"",
		"curl -sS -L -d 'hello=world' "+server.URL+"/start",
		interp.Network(interp.OSNetworkDialer{}),
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "ok" {
		t.Fatalf("stdout = %q, want %q", stdout, "ok")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if startMethod != http.MethodPost {
		t.Fatalf("start method = %q, want %q", startMethod, http.MethodPost)
	}
	if startBody != "hello=world" {
		t.Fatalf("start body = %q, want %q", startBody, "hello=world")
	}
	if finalMethod != http.MethodGet {
		t.Fatalf("final method = %q, want %q", finalMethod, http.MethodGet)
	}
	if finalBody != "" {
		t.Fatalf("final body = %q, want empty", finalBody)
	}
}

func TestCurlKeepsExplicitRequestMethodAcrossRedirects(t *testing.T) {
	var (
		finalMethod string
		finalBody   string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll error: %v", err)
		}
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			finalMethod = r.Method
			finalBody = string(body)
			fmt.Fprint(w, "ok")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		"",
		"curl -sS -L -X POST -d 'hello=world' "+server.URL+"/start",
		interp.Network(interp.OSNetworkDialer{}),
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "ok" {
		t.Fatalf("stdout = %q, want %q", stdout, "ok")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if finalMethod != http.MethodPost {
		t.Fatalf("final method = %q, want %q", finalMethod, http.MethodPost)
	}
	if finalBody != "hello=world" {
		t.Fatalf("final body = %q, want %q", finalBody, "hello=world")
	}
}

func TestCurlSupportsPost302RedirectOption(t *testing.T) {
	var (
		finalMethod string
		finalBody   string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll error: %v", err)
		}
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			finalMethod = r.Method
			finalBody = string(body)
			fmt.Fprint(w, "ok")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		"",
		"curl -sS -L --post302 -d 'hello=world' "+server.URL+"/start",
		interp.Network(interp.OSNetworkDialer{}),
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "ok" {
		t.Fatalf("stdout = %q, want %q", stdout, "ok")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if finalMethod != http.MethodPost {
		t.Fatalf("final method = %q, want %q", finalMethod, http.MethodPost)
	}
	if finalBody != "hello=world" {
		t.Fatalf("final body = %q, want %q", finalBody, "hello=world")
	}
}

func TestCurlStripsSensitiveHeadersAcrossOrigins(t *testing.T) {
	var (
		startAuthorization string
		startCookie        string
		finalAuthorization string
		finalCookie        string
	)
	finalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		finalAuthorization = r.Header.Get("Authorization")
		finalCookie = r.Header.Get("Cookie")
		fmt.Fprint(w, "final")
	}))
	defer finalServer.Close()

	redirectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startAuthorization = r.Header.Get("Authorization")
		startCookie = r.Header.Get("Cookie")
		http.Redirect(w, r, finalServer.URL+"/final", http.StatusFound)
	}))
	defer redirectServer.Close()

	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		"",
		"curl -sS -L -H 'Authorization: Bearer secret' -H 'Cookie: session=abc' "+redirectServer.URL,
		interp.Network(interp.OSNetworkDialer{}),
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "final" {
		t.Fatalf("stdout = %q, want %q", stdout, "final")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if startAuthorization != "Bearer secret" {
		t.Fatalf("start authorization = %q, want %q", startAuthorization, "Bearer secret")
	}
	if startCookie != "session=abc" {
		t.Fatalf("start cookie = %q, want %q", startCookie, "session=abc")
	}
	if finalAuthorization != "" {
		t.Fatalf("final authorization = %q, want empty after cross-origin redirect", finalAuthorization)
	}
	if finalCookie != "" {
		t.Fatalf("final cookie = %q, want empty after cross-origin redirect", finalCookie)
	}
}

func TestCurlLocationTrustedKeepsSensitiveHeadersAcrossOrigins(t *testing.T) {
	var (
		finalAuthorization string
		finalCookie        string
	)
	finalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		finalAuthorization = r.Header.Get("Authorization")
		finalCookie = r.Header.Get("Cookie")
		fmt.Fprint(w, "final")
	}))
	defer finalServer.Close()

	redirectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, finalServer.URL+"/final", http.StatusFound)
	}))
	defer redirectServer.Close()

	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		"",
		"curl -sS --location-trusted -H 'Authorization: Bearer secret' -H 'Cookie: session=abc' "+redirectServer.URL,
		interp.Network(interp.OSNetworkDialer{}),
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "final" {
		t.Fatalf("stdout = %q, want %q", stdout, "final")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if finalAuthorization != "Bearer secret" {
		t.Fatalf("final authorization = %q, want %q", finalAuthorization, "Bearer secret")
	}
	if finalCookie != "session=abc" {
		t.Fatalf("final cookie = %q, want %q", finalCookie, "session=abc")
	}
}

func TestCurlDumpHeaderIncludesRedirectChain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			w.Header().Set("X-Start", "yes")
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			w.Header().Set("X-Final", "ok")
			fmt.Fprint(w, "response-body")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		dir,
		"",
		"curl -L -D headers.txt -o response.txt "+server.URL+"/start",
		interp.Network(interp.OSNetworkDialer{}),
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}

	headers, err := os.ReadFile(filepath.Join(dir, "headers.txt"))
	if err != nil {
		t.Fatalf("ReadFile headers error: %v", err)
	}
	headerText := string(headers)
	if !strings.Contains(headerText, "302 Found\r\n") {
		t.Fatalf("headers = %q, want redirect status line", headerText)
	}
	if !strings.Contains(headerText, "X-Start: yes\r\n") {
		t.Fatalf("headers = %q, want redirect header", headerText)
	}
	if !strings.Contains(headerText, "200 OK\r\n") {
		t.Fatalf("headers = %q, want final status line", headerText)
	}
	if !strings.Contains(headerText, "X-Final: ok\r\n") {
		t.Fatalf("headers = %q, want final header", headerText)
	}

	responseBody, err := os.ReadFile(filepath.Join(dir, "response.txt"))
	if err != nil {
		t.Fatalf("ReadFile response error: %v", err)
	}
	if string(responseBody) != "response-body" {
		t.Fatalf("response file = %q, want %q", string(responseBody), "response-body")
	}
}

func TestCurlFollowsMoreThanThirtyRedirectsByDefault(t *testing.T) {
	var (
		server   *httptest.Server
		requests int
	)
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests <= 35 {
			http.Redirect(w, r, server.URL, http.StatusFound)
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		"",
		"curl -sS -L "+server.URL,
		interp.Network(interp.OSNetworkDialer{}),
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "ok" {
		t.Fatalf("stdout = %q, want %q", stdout, "ok")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if requests != 36 {
		t.Fatalf("requests = %d, want %d", requests, 36)
	}
}

func TestCurlSupportsMaxRedirs(t *testing.T) {
	var (
		server   *httptest.Server
		requests int
	)
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests <= 2 {
			http.Redirect(w, r, server.URL, http.StatusFound)
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		"",
		"curl -sS -L --max-redirs 1 "+server.URL,
		interp.Network(interp.OSNetworkDialer{}),
	)
	if err == nil {
		t.Fatalf("expected curl to fail after reaching max redirects")
	}
	if !errors.Is(err, interp.ExitStatus(47)) {
		t.Fatalf("error = %v, want exit status 47", err)
	}
	if !strings.Contains(err.Error(), "curl: maximum (1) redirects followed") {
		t.Fatalf("error = %v, want max redirect failure", err)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if stderr != "curl: maximum (1) redirects followed\n" {
		t.Fatalf("stderr = %q, want max redirect message", stderr)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want %d", requests, 2)
	}
}

func TestCurlSupportsShowHeadersAlias(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Reply", "ok")
		fmt.Fprint(w, "response-body")
	}))
	defer server.Close()

	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		"",
		"curl --show-headers "+server.URL,
		interp.Network(interp.OSNetworkDialer{}),
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "HTTP/1.1 200 OK\r\n") {
		t.Fatalf("stdout = %q, want HTTP status line", stdout)
	}
	if !strings.Contains(stdout, "X-Reply: ok\r\n") {
		t.Fatalf("stdout = %q, want response header", stdout)
	}
	if !strings.Contains(stdout, "response-body") {
		t.Fatalf("stdout = %q, want response body", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
}

func TestCurlHeadRejectsDataOptions(t *testing.T) {
	stdout, stderr, err := runCoreutilsScript(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		"",
		"curl -I -d 'hello=world' http://example.com",
	)
	if err == nil {
		t.Fatalf("expected curl to reject incompatible options")
	}
	if !strings.Contains(err.Error(), "--head is mutually exclusive") {
		t.Fatalf("error = %v, want mutual exclusion error", err)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
}

func TestJqSupportsNullInputAndExitStatus(t *testing.T) {
	stdout, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, t.TempDir(), "", `jq -nc '{"answer":42}'`)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "{\"answer\":42}\n" {
		t.Fatalf("stdout = %q, want compact object output", stdout)
	}

	_, stderr, err = runCoreutilsScript(t, interp.OSFileSystem{}, t.TempDir(), "{\"other\":1}\n", "jq -e .missing")
	if !errors.Is(err, interp.ExitStatus(1)) {
		t.Fatalf("error = %v, want exit status 1\nstderr=%s", err, stderr)
	}
}

func TestJqKeepsUnsafeLoadersDisabled(t *testing.T) {
	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		"",
		`jq -n 'env'`,
		interp.Env(expand.ListEnviron("SECRET=present")),
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "{}\n" {
		t.Fatalf("stdout = %q, want empty env object", stdout)
	}

	_, stderr, err = runCoreutilsScript(t, interp.OSFileSystem{}, t.TempDir(), "", `jq -n 'input'`)
	if err == nil || !strings.Contains(err.Error(), "input(s)/0 is not allowed") {
		t.Fatalf("error = %v, want input restriction\nstderr=%s", err, stderr)
	}

	_, stderr, err = runCoreutilsScript(t, interp.OSFileSystem{}, t.TempDir(), "", `jq -n 'import "module1" as m; .'`)
	if err == nil || !strings.Contains(err.Error(), `cannot load module: "module1"`) {
		t.Fatalf("error = %v, want module loading restriction\nstderr=%s", err, stderr)
	}
}

func TestAwkReadsFromStdinAndFiles(t *testing.T) {
	stdout, stderr, err := runCoreutilsScript(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		"left,right\nup,down\n",
		"awk -F , '{ print $2 }'",
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "right\ndown\n" {
		t.Fatalf("stdout = %q, want %q", stdout, "right\ndown\n")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "input.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fsys := &trackingFS{OSFileSystem: interp.OSFileSystem{}}
	stdout, stderr, err = runCoreutilsScript(t, fsys, dir, "", "awk '{ print $0 }' input.txt")
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "hello\n" {
		t.Fatalf("stdout = %q, want %q", stdout, "hello\n")
	}
	if fsys.openCount == 0 {
		t.Fatalf("expected Open to be used")
	}
}

func TestAwkTracksFilenameAndFNRAcrossRestrictedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "first.txt"), []byte("alpha\nomega"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "second.txt"), []byte("beta\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runCoreutilsScript(
		t,
		interp.OSFileSystem{},
		dir,
		"",
		`awk '{ printf "%s:%d:%d:%s\n", FILENAME, FNR, NR, $0 }' first.txt second.txt`,
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	want := "first.txt:1:1:alpha\nfirst.txt:2:2:omega\nsecond.txt:1:3:beta\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
}

func TestAwkPopulatesArgvForRestrictedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "input.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runCoreutilsScript(
		t,
		interp.OSFileSystem{},
		dir,
		"",
		`awk 'BEGIN { printf "%s:%s\n", ARGV[1], ARGV[2] }' input.txt -`,
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "input.txt:-\n" {
		t.Fatalf("stdout = %q, want %q", stdout, "input.txt:-\n")
	}
}

func TestAwkUsesInterpreterEnvironment(t *testing.T) {
	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		"",
		`awk 'BEGIN { printf "<%s><%s>\n", ENVIRON["SAFE"], ENVIRON["HOME"] }'`,
		interp.Env(expand.ListEnviron("SAFE=present")),
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "<present><>\n" {
		t.Fatalf("stdout = %q, want sandbox environment only", stdout)
	}
}

func TestAwkRejectsRestrictedIO(t *testing.T) {
	tests := []struct {
		name   string
		script string
		want   string
	}{
		{
			name:   "system",
			script: `awk 'BEGIN { system("echo hi") }'`,
			want:   "can't call system() due to NoExec",
		},
		{
			name:   "file read",
			script: `awk 'BEGIN { getline x < "in" }'`,
			want:   "can't read from file due to NoFileReads",
		},
		{
			name:   "file write",
			script: `awk 'BEGIN { print "hi" > "out" }'`,
			want:   "can't write to file due to NoFileWrites",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, t.TempDir(), "", tt.script)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q\nstderr=%s", err, tt.want, stderr)
			}
		})
	}
}

func TestAwkRejectsUnsupportedRestrictedFileOperandFeatures(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "input.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		script string
		want   string
	}{
		{
			name:   "getline",
			script: `awk '{ getline; print }' input.txt`,
			want:   "getline is not supported with file operands in restricted mode",
		},
		{
			name:   "nextfile",
			script: `awk '{ nextfile }' input.txt`,
			want:   "nextfile is not supported with file operands in restricted mode",
		},
		{
			name:   "arg assignment",
			script: `awk 'BEGIN { print x }' x=1 input.txt`,
			want:   "var=value operands are not supported with file operands in restricted mode; use -v",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, dir, "", tt.script)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q\nstderr=%s", err, tt.want, stderr)
			}
		})
	}
}

func TestCommandsEmitUsageForMissingRequiredOperands(t *testing.T) {
	tests := []struct {
		name       string
		script     string
		wantStderr string
		wantStatus interp.ExitStatus
	}{
		{
			name:       "awk",
			script:     "awk",
			wantStderr: "usage: awk [-F fs] [-v var=value] [-f progfile | 'prog']\n",
			wantStatus: interp.ExitStatus(1),
		},
		{
			name:       "grep",
			script:     "grep",
			wantStderr: "usage: grep [-E|-F] [-R|-r] [-ivncloq] [-e pattern]... [pattern] [file ...]\n",
			wantStatus: interp.ExitStatus(2),
		},
		{
			name:       "touch",
			script:     "touch",
			wantStderr: "usage: touch [-acm] [-c] [-d time] [-t timestamp] file ...\n",
			wantStatus: interp.ExitStatus(1),
		},
		{
			name:       "mkdir",
			script:     "mkdir",
			wantStderr: "usage: mkdir [-pv] [-m mode] directory ...\n",
			wantStatus: interp.ExitStatus(1),
		},
		{
			name:       "tr",
			script:     "tr",
			wantStderr: "usage: tr [-ds] string1 [string2]\n",
			wantStatus: interp.ExitStatus(1),
		},
		{
			name:       "cut",
			script:     "cut",
			wantStderr: "usage: cut (-b list | -c list | -f list) [-d delim] [-s] [file ...]\n",
			wantStatus: interp.ExitStatus(1),
		},
		{
			name:       "comm",
			script:     "comm",
			wantStderr: "usage: comm [-123] file1 file2\n",
			wantStatus: interp.ExitStatus(1),
		},
		{
			name:       "diff",
			script:     "diff",
			wantStderr: "usage: diff [-u] [-q] file1 file2\n",
			wantStatus: interp.ExitStatus(1),
		},
		{
			name:       "chmod",
			script:     "chmod",
			wantStderr: "usage: chmod [-R] mode file ...\n       chmod [-R] --reference=reference_file file ...\n",
			wantStatus: interp.ExitStatus(1),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, t.TempDir(), "", tt.script)
			if stdout != "" {
				t.Fatalf("stdout = %q, want empty", stdout)
			}
			if stderr != tt.wantStderr {
				t.Fatalf("stderr = %q, want %q", stderr, tt.wantStderr)
			}
			if !errors.Is(err, tt.wantStatus) {
				t.Fatalf("error = %v, want exit status %d", err, tt.wantStatus)
			}
		})
	}
}

func TestSedDefaultsToIdentityScript(t *testing.T) {
	stdout, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, t.TempDir(), "left\nright\n", "sed")
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if stdout != "left\nright\n" {
		t.Fatalf("stdout = %q, want %q", stdout, "left\nright\n")
	}
}

func TestJqDefaultsToIdentityFilter(t *testing.T) {
	stdout, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, t.TempDir(), "{\"foo\":1}\n", "jq")
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if stdout != "{\n  \"foo\": 1\n}\n" {
		t.Fatalf("stdout = %q, want pretty-printed input", stdout)
	}
}

func TestSedSupportsLineRanges(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lines.txt"), []byte("one\ntwo\nthree\nfour\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, dir, "", "sed -n '2,3p' lines.txt")
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "two\nthree\n" {
		t.Fatalf("stdout = %q, want %q", stdout, "two\nthree\n")
	}
}

func TestSedSupportsRegexDeleteRanges(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lines.txt"), []byte("keep\nstart\nmiddle\nstop\nkeep2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, dir, "", "sed '/start/,/stop/d' lines.txt")
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "keep\nkeep2\n" {
		t.Fatalf("stdout = %q, want %q", stdout, "keep\nkeep2\n")
	}
}

func TestTrSupportsTranslationAndDeletion(t *testing.T) {
	t.Run("translate", func(t *testing.T) {
		stdout, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, t.TempDir(), "alpha\n", "tr a-z A-Z")
		if err != nil {
			t.Fatalf("run error: %v\nstderr=%s", err, stderr)
		}
		if stdout != "ALPHA\n" {
			t.Fatalf("stdout = %q, want %q", stdout, "ALPHA\n")
		}
	})

	t.Run("delete", func(t *testing.T) {
		stdout, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, t.TempDir(), "a b c\n", "tr -d ' '")
		if err != nil {
			t.Fatalf("run error: %v\nstderr=%s", err, stderr)
		}
		if stdout != "abc\n" {
			t.Fatalf("stdout = %q, want %q", stdout, "abc\n")
		}
	})
}

func TestCommUsesFilesystemAdapter(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "left.txt"), []byte("alpha\nbeta\nsame\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "right.txt"), []byte("beta\ngamma\nsame\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fsys := &trackingFS{}
	stdout, stderr, err := runCoreutilsScript(t, fsys, dir, "", "comm left.txt right.txt")
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "alpha\n\t\tbeta\n\tgamma\n\t\tsame\n" {
		t.Fatalf("stdout = %q, want comm output", stdout)
	}
	if fsys.openCount < 2 {
		t.Fatalf("expected Open to be used for both files")
	}
}

func TestDiffUsesFilesystemAdapter(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "left.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "right.txt"), []byte("one\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fsys := &trackingFS{}
	stdout, stderr, err := runCoreutilsScript(t, fsys, dir, "", "diff -u left.txt right.txt")
	if !errors.Is(err, interp.ExitStatus(1)) {
		t.Fatalf("error = %v, want exit status 1\nstderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "--- left.txt") || !strings.Contains(stdout, "+++ right.txt") || !strings.Contains(stdout, "-two") || !strings.Contains(stdout, "+three") {
		t.Fatalf("stdout = %q, want unified diff output", stdout)
	}
	if fsys.openCount < 2 {
		t.Fatalf("expected Open to be used for both files")
	}
}

func TestCurlSupportsURLBeforeOutputOption(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "response-body")
	}))
	defer server.Close()

	dir := t.TempDir()
	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		dir,
		"",
		"curl "+server.URL+" -o response.txt",
		interp.Network(interp.OSNetworkDialer{}),
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}

	data, err := os.ReadFile(filepath.Join(dir, "response.txt"))
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if string(data) != "response-body" {
		t.Fatalf("response file = %q, want %q", string(data), "response-body")
	}
}

func TestCurlSupportsHeadersAfterURL(t *testing.T) {
	t.Parallel()

	var gotHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Test")
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	stdout, stderr, err := runCoreutilsScriptWithOptions(
		t,
		interp.OSFileSystem{},
		t.TempDir(),
		"",
		"curl "+server.URL+" -H 'X-Test: yes'",
		interp.Network(interp.OSNetworkDialer{}),
	)
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "ok" {
		t.Fatalf("stdout = %q, want %q", stdout, "ok")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if gotHeader != "yes" {
		t.Fatalf("header = %q, want %q", gotHeader, "yes")
	}
}

func TestLsSupportsPostOperandOptions(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "listdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "listdir", "item.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, dir, "", "ls listdir -l")
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "item.txt") {
		t.Fatalf("stdout = %q, want long listing containing file name", stdout)
	}
}

func TestCpSupportsPostOperandOptions(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "source.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, dir, "", "cp source.txt copy.txt -v")
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "copy.txt") {
		t.Fatalf("stdout = %q, want verbose destination path", stdout)
	}
	data, err := os.ReadFile(filepath.Join(dir, "copy.txt"))
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("copy contents = %q, want %q", string(data), "hello")
	}
}

func TestHeadSupportsPostOperandOptions(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sample.txt"), []byte("first\nsecond\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, dir, "", "head sample.txt -n 1")
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "first\n" {
		t.Fatalf("stdout = %q, want %q", stdout, "first\n")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
}

func TestGzipSupportsPostOperandOptions(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compress.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, dir, "", "gzip compress.txt -k")
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "compress.txt")); err != nil {
		t.Fatalf("expected source file to remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "compress.txt.gz")); err != nil {
		t.Fatalf("expected compressed file: %v", err)
	}
}

func TestEnvStopsOptionParsingAtCommandBoundary(t *testing.T) {
	t.Parallel()

	stdout, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, t.TempDir(), "", "env printf '%s' -i")
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "-i" {
		t.Fatalf("stdout = %q, want %q", stdout, "-i")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
}

func TestXargsStopsOptionParsingAtCommandBoundary(t *testing.T) {
	t.Parallel()

	stdout, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, t.TempDir(), "", "xargs printf '%s' -n")
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "-n" {
		t.Fatalf("stdout = %q, want %q", stdout, "-n")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
}

func TestCatSupportsDoubleDashLiteralOperand(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "-literal.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runCoreutilsScript(t, interp.OSFileSystem{}, dir, "", "cat -- -literal.txt")
	if err != nil {
		t.Fatalf("run error: %v\nstderr=%s", err, stderr)
	}
	if stdout != "hello" {
		t.Fatalf("stdout = %q, want %q", stdout, "hello")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
}

func mustHTTPClientFactory(t *testing.T, dialer interp.NetworkDialer, rules []interp.HTTPHeaderRule) interp.HTTPClientFactory {
	t.Helper()
	factory, err := interp.NewHTTPClientFactory(dialer, rules)
	if err != nil {
		t.Fatalf("NewHTTPClientFactory() error = %v", err)
	}
	return factory
}

type hostMapDialer struct {
	hostToAddress map[string]string
}

func (d *hostMapDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host := address
	if parsedHost, _, err := net.SplitHostPort(address); err == nil {
		host = parsedHost
	}
	target, ok := d.hostToAddress[host]
	if !ok {
		return nil, fmt.Errorf("unexpected dial host %q", host)
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, network, target)
}
