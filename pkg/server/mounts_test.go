package server

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/richardartoul/swarmd/pkg/sh/memfs"
)

func TestManagedAgentMountFromSpecFollowsTopLevelSymlink(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("symlink tests require POSIX-style symlink support")
	}

	configRoot := t.TempDir()
	targetPath := filepath.Join(configRoot, "seed.txt")
	if err := os.WriteFile(targetPath, []byte("seed"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", targetPath, err)
	}
	resolvedTargetPath, err := filepath.EvalSymlinks(targetPath)
	if err != nil {
		t.Fatalf("filepath.EvalSymlinks(%q) error = %v", targetPath, err)
	}
	symlinkPath := filepath.Join(configRoot, "seed-link.txt")
	if err := os.Symlink(targetPath, symlinkPath); err != nil {
		t.Fatalf("os.Symlink(%q, %q) error = %v", targetPath, symlinkPath, err)
	}

	mount, err := managedAgentMountFromSpec(filepath.Join(configRoot, "agent.yaml"), AgentMountSpec{
		Path: "mounted/seed.txt",
		Source: AgentMountSourceSpec{
			Path: "seed-link.txt",
		},
	})
	if err != nil {
		t.Fatalf("managedAgentMountFromSpec() error = %v", err)
	}
	if got := mount.kind(); got != managedAgentMountKindFile {
		t.Fatalf("mount.kind() = %q, want %q", got, managedAgentMountKindFile)
	}
	if got := mount.Source.ResolvedPath; got != resolvedTargetPath {
		t.Fatalf("mount.Source.ResolvedPath = %q, want %q", got, resolvedTargetPath)
	}
}

func TestManagedAgentMountFromSpecRejectsNestedSymlinkInDirectorySource(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("symlink tests require POSIX-style symlink support")
	}

	configRoot := t.TempDir()
	treeRoot := filepath.Join(configRoot, "tree")
	if err := os.MkdirAll(treeRoot, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", treeRoot, err)
	}
	targetPath := filepath.Join(configRoot, "seed.txt")
	if err := os.WriteFile(targetPath, []byte("seed"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", targetPath, err)
	}
	if err := os.Symlink(targetPath, filepath.Join(treeRoot, "seed-link.txt")); err != nil {
		t.Fatalf("os.Symlink() error = %v", err)
	}

	_, err := managedAgentMountFromSpec(filepath.Join(configRoot, "agent.yaml"), AgentMountSpec{
		Path: "mounted/tree",
		Source: AgentMountSourceSpec{
			Path: "tree",
		},
	})
	if err == nil {
		t.Fatal("managedAgentMountFromSpec() error = nil, want nested symlink rejection")
	}
	if !strings.Contains(err.Error(), "directory tree of regular files only") {
		t.Fatalf("managedAgentMountFromSpec() error = %v, want directory tree validation failure", err)
	}
}

func TestMaterializeAgentMountsRejectsLegacyEmbeddedPathMountConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mount   managedAgentMount
		wantErr string
	}{
		{
			name: "path mount missing resolved path",
			mount: managedAgentMount{
				Path: "mounted/seed.txt",
				Source: managedAgentMountSource{
					Path: "../seed.txt",
				},
				Kind:          managedAgentMountKindFile,
				ContentBase64: base64.StdEncoding.EncodeToString([]byte("legacy")),
			},
			wantErr: "re-sync or recreate the agent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys, err := memfs.New("/workspace")
			if err != nil {
				t.Fatalf("memfs.New() error = %v", err)
			}
			err = materializeAgentMounts(fsys, []managedAgentMount{tt.mount}, nil)
			if err == nil {
				t.Fatal("materializeAgentMounts() error = nil, want legacy mount rejection")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("materializeAgentMounts() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestMaterializeAgentMountsRejectsRemovedPathSourceAtStartup(t *testing.T) {
	t.Parallel()

	sourcePath := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(sourcePath, []byte("seed"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", sourcePath, err)
	}
	if err := os.Remove(sourcePath); err != nil {
		t.Fatalf("os.Remove(%q) error = %v", sourcePath, err)
	}
	fsys, err := memfs.New("/workspace")
	if err != nil {
		t.Fatalf("memfs.New() error = %v", err)
	}

	err = materializeAgentMounts(fsys, []managedAgentMount{{
		Path: "mounted/source.txt",
		Source: managedAgentMountSource{
			Path:         sourcePath,
			ResolvedPath: sourcePath,
		},
		Kind: managedAgentMountKindFile,
	}}, nil)
	if err == nil {
		t.Fatal("materializeAgentMounts() error = nil, want missing source failure")
	}
	if !strings.Contains(err.Error(), "stat") {
		t.Fatalf("materializeAgentMounts() error = %v, want stat failure", err)
	}
}

func TestMaterializeAgentMountsRejectsUnreadablePathSourceAtStartup(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("permission-based readability test requires POSIX-style file modes")
	}

	sourcePath := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(sourcePath, []byte("seed"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", sourcePath, err)
	}
	if err := os.Chmod(sourcePath, 0); err != nil {
		t.Fatalf("os.Chmod(%q, 0) error = %v", sourcePath, err)
	}
	defer os.Chmod(sourcePath, 0o644)
	fsys, err := memfs.New("/workspace")
	if err != nil {
		t.Fatalf("memfs.New() error = %v", err)
	}

	err = materializeAgentMounts(fsys, []managedAgentMount{{
		Path: "mounted/source.txt",
		Source: managedAgentMountSource{
			Path:         sourcePath,
			ResolvedPath: sourcePath,
		},
		Kind: managedAgentMountKindFile,
	}}, nil)
	if err == nil {
		t.Skip("materializeAgentMounts() unexpectedly succeeded with mode 000 source file")
	}
	if !strings.Contains(err.Error(), "open") {
		t.Fatalf("materializeAgentMounts() error = %v, want open failure", err)
	}
}

func TestMaterializeAgentMountsRejectsPathSourceKindMismatchAtStartup(t *testing.T) {
	t.Parallel()

	sourceDir := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", sourceDir, err)
	}
	fsys, err := memfs.New("/workspace")
	if err != nil {
		t.Fatalf("memfs.New() error = %v", err)
	}

	err = materializeAgentMounts(fsys, []managedAgentMount{{
		Path: "mounted/source.txt",
		Source: managedAgentMountSource{
			Path:         sourceDir,
			ResolvedPath: sourceDir,
		},
		Kind: managedAgentMountKindFile,
	}}, nil)
	if err == nil {
		t.Fatal("materializeAgentMounts() error = nil, want source kind mismatch failure")
	}
	if !strings.Contains(err.Error(), `changed from "file" to "directory"`) {
		t.Fatalf("materializeAgentMounts() error = %v, want kind mismatch", err)
	}
}
