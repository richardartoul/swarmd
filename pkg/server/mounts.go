package server

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
)

const (
	managedAgentMountKindFile      = "file"
	managedAgentMountKindDirectory = "directory"
)

type AgentMountSpec struct {
	Path        string               `yaml:"path,omitempty" json:"path,omitempty"`
	Description string               `yaml:"description,omitempty" json:"description,omitempty"`
	Source      AgentMountSourceSpec `yaml:"source,omitempty" json:"source,omitempty"`
}

type AgentMountSourceSpec struct {
	Path   string  `yaml:"path,omitempty" json:"path,omitempty"`
	EnvVar string  `yaml:"env_var,omitempty" json:"env_var,omitempty"`
	Inline *string `yaml:"inline,omitempty" json:"inline,omitempty"`
}

type managedAgentMount struct {
	Path          string                   `json:"path,omitempty"`
	Description   string                   `json:"description,omitempty"`
	Source        managedAgentMountSource  `json:"source,omitempty"`
	Kind          string                   `json:"kind,omitempty"`
	ContentBase64 string                   `json:"content_base64,omitempty"`
	Entries       []managedAgentMountEntry `json:"entries,omitempty"`
}

type managedAgentMountSource struct {
	Path   string  `json:"path,omitempty"`
	EnvVar string  `json:"env_var,omitempty"`
	Inline *string `json:"inline,omitempty"`
}

type managedAgentMountEntry struct {
	Path          string `json:"path,omitempty"`
	Kind          string `json:"kind,omitempty"`
	ContentBase64 string `json:"content_base64,omitempty"`
}

func validateAgentMounts(spec AgentSpec) error {
	seen := make(map[string]int, len(spec.Mounts))
	for i, mount := range spec.Mounts {
		targetPath, err := normalizeAgentMountTargetPath(mount.Path)
		if err != nil {
			return fmt.Errorf("mounts[%d].path invalid: %w", i, err)
		}
		if previous, ok := seen[targetPath]; ok {
			return fmt.Errorf("mounts[%d].path duplicates mounts[%d]", i, previous)
		}
		seen[targetPath] = i
		if _, err := managedAgentMountFromSpec(spec.SourcePath, mount); err != nil {
			switch {
			case strings.Contains(err.Error(), "exactly one of source.path, source.env_var, or source.inline"):
				return fmt.Errorf("mounts[%d] %w", i, err)
			case strings.Contains(err.Error(), "source.path"):
				return fmt.Errorf("mounts[%d].source.path invalid: %w", i, err)
			default:
				return fmt.Errorf("mounts[%d] invalid: %w", i, err)
			}
		}
	}
	return nil
}

func managedAgentMounts(spec AgentSpec) ([]managedAgentMount, error) {
	if len(spec.Mounts) == 0 {
		return nil, nil
	}
	mounts := make([]managedAgentMount, 0, len(spec.Mounts))
	for i, mount := range spec.Mounts {
		managedMount, err := managedAgentMountFromSpec(spec.SourcePath, mount)
		if err != nil {
			return nil, fmt.Errorf("load mounts[%d] content: %w", i, err)
		}
		mounts = append(mounts, managedMount)
	}
	return mounts, nil
}

func normalizeAgentMountTargetPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("must not be empty")
	}
	if volume := filepath.VolumeName(raw); volume != "" {
		return "", fmt.Errorf("must not include a volume name")
	}
	cleaned := filepath.Clean(strings.TrimLeft(raw, `/\`))
	if cleaned == "." || cleaned == "" {
		return "", fmt.Errorf("must not resolve to the sandbox root")
	}
	parentPrefix := ".." + string(os.PathSeparator)
	if cleaned == ".." || strings.HasPrefix(cleaned, parentPrefix) {
		return "", fmt.Errorf("must not escape the sandbox root")
	}
	return cleaned, nil
}

func resolveAgentMountSourcePath(specPath, sourcePath string) (string, error) {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return "", fmt.Errorf("must not be empty")
	}
	if filepath.IsAbs(sourcePath) {
		return filepath.Clean(sourcePath), nil
	}
	baseDir := filepath.Dir(specPath)
	if strings.TrimSpace(baseDir) == "" {
		return "", fmt.Errorf("spec source path must not be empty")
	}
	return filepath.Clean(filepath.Join(baseDir, sourcePath)), nil
}

func managedAgentMountFromSpec(specPath string, mount AgentMountSpec) (managedAgentMount, error) {
	targetPath, err := normalizeAgentMountTargetPath(mount.Path)
	if err != nil {
		return managedAgentMount{}, fmt.Errorf("path invalid: %w", err)
	}
	managedMount := managedAgentMount{
		Path:        targetPath,
		Description: strings.TrimSpace(mount.Description),
		Source: managedAgentMountSource{
			Path:   strings.TrimSpace(mount.Source.Path),
			EnvVar: strings.TrimSpace(mount.Source.EnvVar),
			Inline: mount.Source.Inline,
		},
	}
	hasSourcePath := strings.TrimSpace(mount.Source.Path) != ""
	hasEnvVar := strings.TrimSpace(mount.Source.EnvVar) != ""
	hasInlineContent := mount.Source.Inline != nil
	switch {
	case mountSourceSelectionCount(hasSourcePath, hasEnvVar, hasInlineContent) != 1:
		return managedMount, fmt.Errorf("must set exactly one of source.path, source.env_var, or source.inline")
	case hasInlineContent:
		managedMount.Kind = managedAgentMountKindFile
		managedMount.ContentBase64 = base64.StdEncoding.EncodeToString([]byte(*mount.Source.Inline))
		return managedMount, nil
	case hasEnvVar:
		managedMount.Kind = managedAgentMountKindFile
		return managedMount, nil
	default:
		kind, contentBase64, entries, err := loadManagedAgentMountSource(specPath, mount.Source.Path)
		if err != nil {
			return managedMount, err
		}
		managedMount.Kind = kind
		managedMount.ContentBase64 = contentBase64
		managedMount.Entries = entries
		return managedMount, nil
	}
}

func loadManagedAgentMountSource(specPath, sourcePath string) (string, string, []managedAgentMountEntry, error) {
	resolvedSourcePath, err := resolveAgentMountSourcePath(specPath, sourcePath)
	if err != nil {
		return "", "", nil, err
	}
	info, err := os.Stat(resolvedSourcePath)
	if err != nil {
		return "", "", nil, fmt.Errorf("stat %q: %w", resolvedSourcePath, err)
	}
	switch {
	case info.Mode().IsRegular():
		data, err := os.ReadFile(resolvedSourcePath)
		if err != nil {
			return "", "", nil, fmt.Errorf("read %q: %w", resolvedSourcePath, err)
		}
		return managedAgentMountKindFile, base64.StdEncoding.EncodeToString(data), nil, nil
	case info.IsDir():
		entries, err := loadManagedAgentMountDirectoryEntries(resolvedSourcePath)
		if err != nil {
			return "", "", nil, err
		}
		return managedAgentMountKindDirectory, "", entries, nil
	default:
		return "", "", nil, fmt.Errorf("%q is %s, want regular file or directory", resolvedSourcePath, mountSourceFileKind(info.Mode()))
	}
}

func mountSourceSelectionCount(values ...bool) int {
	count := 0
	for _, value := range values {
		if value {
			count++
		}
	}
	return count
}

func loadManagedAgentMountDirectoryEntries(root string) ([]managedAgentMountEntry, error) {
	var entries []managedAgentMountEntry
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relPath = filepath.ToSlash(relPath)
		switch {
		case entry.IsDir():
			entries = append(entries, managedAgentMountEntry{
				Path: relPath,
				Kind: managedAgentMountKindDirectory,
			})
			return nil
		case entry.Type()&fs.ModeSymlink != 0:
			return fmt.Errorf("%q is a symlink, want directory tree of regular files only", path)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%q is %s, want directory tree of regular files only", path, mountSourceFileKind(info.Mode()))
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %q: %w", path, err)
		}
		entries = append(entries, managedAgentMountEntry{
			Path:          relPath,
			Kind:          managedAgentMountKindFile,
			ContentBase64: base64.StdEncoding.EncodeToString(data),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func mountSourceFileKind(mode fs.FileMode) string {
	switch {
	case mode.IsDir():
		return "a directory"
	case mode&fs.ModeSymlink != 0:
		return "a symlink"
	default:
		return "not a regular file"
	}
}

func (mount managedAgentMount) contentBytes() ([]byte, error) {
	return mount.contentBytesWithLookup(os.Getenv)
}

func (mount managedAgentMount) contentBytesWithLookup(lookupEnv func(string) string) ([]byte, error) {
	if envVar := strings.TrimSpace(mount.Source.EnvVar); envVar != "" {
		if lookupEnv == nil {
			lookupEnv = os.Getenv
		}
		value := lookupEnv(envVar)
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("environment variable %q for mount %q is empty or not set", envVar, mount.Path)
		}
		return []byte(value), nil
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(mount.ContentBase64))
	if err != nil {
		return nil, fmt.Errorf("decode %q content: %w", mount.Path, err)
	}
	return data, nil
}

func (mount managedAgentMount) kind() string {
	if strings.TrimSpace(mount.Kind) == "" {
		return managedAgentMountKindFile
	}
	return mount.Kind
}

func (entry managedAgentMountEntry) contentBytes() ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(entry.ContentBase64))
	if err != nil {
		return nil, fmt.Errorf("decode %q content: %w", entry.Path, err)
	}
	return data, nil
}

func (entry managedAgentMountEntry) kind() string {
	if strings.TrimSpace(entry.Kind) == "" {
		return managedAgentMountKindFile
	}
	return entry.Kind
}

func materializeAgentMounts(fsys sandbox.FileSystem, mounts []managedAgentMount, lookupEnv func(string) string) error {
	if len(mounts) == 0 {
		return nil
	}
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	rootDir, err := fsys.Getwd()
	if err != nil {
		return fmt.Errorf("resolve filesystem root: %w", err)
	}
	for _, mount := range mounts {
		targetPath, err := sandbox.ResolvePath(fsys, rootDir, mount.Path)
		if err != nil {
			return fmt.Errorf("resolve mount target %q: %w", mount.Path, err)
		}
		switch mount.kind() {
		case managedAgentMountKindFile:
			content, err := mount.contentBytesWithLookup(lookupEnv)
			if err != nil {
				return err
			}
			if err := materializeAgentMountFile(fsys, targetPath, content); err != nil {
				return fmt.Errorf("write mount %q: %w", targetPath, err)
			}
		case managedAgentMountKindDirectory:
			if err := materializeAgentMountDirectory(fsys, targetPath, mount.Entries); err != nil {
				return fmt.Errorf("write mount directory %q: %w", targetPath, err)
			}
		default:
			return fmt.Errorf("mount %q has unsupported kind %q", mount.Path, mount.Kind)
		}
	}
	return nil
}

func materializeAgentMountDirectory(fsys sandbox.FileSystem, targetPath string, entries []managedAgentMountEntry) error {
	if err := ensureMountDirectory(fsys, targetPath); err != nil {
		return err
	}
	for _, entry := range entries {
		entryPath, err := sandbox.ResolvePath(fsys, targetPath, filepath.FromSlash(entry.Path))
		if err != nil {
			return fmt.Errorf("resolve directory mount entry %q: %w", entry.Path, err)
		}
		switch entry.kind() {
		case managedAgentMountKindDirectory:
			if err := ensureMountDirectory(fsys, entryPath); err != nil {
				return err
			}
		case managedAgentMountKindFile:
			content, err := entry.contentBytes()
			if err != nil {
				return err
			}
			if err := materializeAgentMountFile(fsys, entryPath, content); err != nil {
				return err
			}
		default:
			return fmt.Errorf("directory mount entry %q has unsupported kind %q", entry.Path, entry.Kind)
		}
	}
	return nil
}

func materializeAgentMountFile(fsys sandbox.FileSystem, targetPath string, content []byte) error {
	if err := ensureMountDirectory(fsys, filepath.Dir(targetPath)); err != nil {
		return err
	}
	if err := ensureMountFileTarget(fsys, targetPath); err != nil {
		return err
	}
	return fsys.WriteFile(targetPath, content, 0o644)
}

func ensureMountDirectory(fsys sandbox.FileSystem, path string) error {
	path = filepath.Clean(path)
	rootPath, err := fsys.Getwd()
	if err != nil {
		return err
	}
	rootPath = filepath.Clean(rootPath)
	if path == rootPath {
		return nil
	}
	relPath, err := filepath.Rel(rootPath, path)
	if err != nil {
		return err
	}
	parentPrefix := ".." + string(os.PathSeparator)
	if relPath == ".." || strings.HasPrefix(relPath, parentPrefix) {
		return fmt.Errorf("directory %q escapes sandbox root %q", path, rootPath)
	}
	currentPath := rootPath
	for _, segment := range strings.Split(relPath, string(os.PathSeparator)) {
		if segment == "" || segment == "." {
			continue
		}
		currentPath = filepath.Join(currentPath, segment)
		info, err := fsys.Lstat(currentPath)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := fsys.Mkdir(currentPath, 0o755); err != nil {
				return err
			}
		case err != nil:
			return err
		case info.IsDir():
		default:
			if err := fsys.RemoveAll(currentPath); err != nil {
				return err
			}
			if err := fsys.Mkdir(currentPath, 0o755); err != nil {
				return err
			}
		}
	}
	return nil
}

func ensureMountFileTarget(fsys sandbox.FileSystem, path string) error {
	info, err := fsys.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case info.Mode().IsRegular():
		return nil
	default:
		return fsys.RemoveAll(path)
	}
}
