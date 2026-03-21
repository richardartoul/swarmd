package server

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
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
	Path          string                  `json:"path,omitempty"`
	Description   string                  `json:"description,omitempty"`
	Source        managedAgentMountSource `json:"source,omitempty"`
	Kind          string                  `json:"kind,omitempty"`
	ContentBase64 string                  `json:"content_base64,omitempty"`
}

type managedAgentMountSource struct {
	Path         string  `json:"path,omitempty"`
	ResolvedPath string  `json:"resolved_path,omitempty"`
	EnvVar       string  `json:"env_var,omitempty"`
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
		hasSourcePath := strings.TrimSpace(mount.Source.Path) != ""
		hasEnvVar := strings.TrimSpace(mount.Source.EnvVar) != ""
		hasInlineContent := mount.Source.Inline != nil
		if mountSourceSelectionCount(hasSourcePath, hasEnvVar, hasInlineContent) != 1 {
			return fmt.Errorf("mounts[%d] must set exactly one of source.path, source.env_var, or source.inline", i)
		}
		if _, err := managedAgentMountFromSpec(spec.SourcePath, mount); err != nil {
			if hasSourcePath {
				return fmt.Errorf("mounts[%d].source.path invalid: %w", i, err)
			}
			return fmt.Errorf("mounts[%d] invalid: %w", i, err)
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
		inspectedSource, err := inspectManagedAgentMountSourcePath(specPath, mount.Source.Path)
		if err != nil {
			return managedMount, err
		}
		managedMount.Kind = inspectedSource.Kind
		managedMount.Source.ResolvedPath = inspectedSource.ResolvedPath
		return managedMount, nil
	}
}

type inspectedManagedAgentMountSource struct {
	Kind         string
	ResolvedPath string
}

func inspectManagedAgentMountSourcePath(specPath, sourcePath string) (inspectedManagedAgentMountSource, error) {
	resolvedSourcePath, err := resolveAgentMountSourcePath(specPath, sourcePath)
	if err != nil {
		return inspectedManagedAgentMountSource{}, err
	}
	return inspectManagedAgentMountResolvedSourcePath(resolvedSourcePath)
}

func inspectManagedAgentMountResolvedSourcePath(sourcePath string) (inspectedManagedAgentMountSource, error) {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return inspectedManagedAgentMountSource{}, fmt.Errorf("must not be empty")
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		return inspectedManagedAgentMountSource{}, fmt.Errorf("stat %q: %w", sourcePath, err)
	}
	resolvedSourcePath, err := filepath.EvalSymlinks(sourcePath)
	if err != nil {
		return inspectedManagedAgentMountSource{}, fmt.Errorf("resolve %q: %w", sourcePath, err)
	}
	resolvedSourcePath = filepath.Clean(resolvedSourcePath)
	switch {
	case info.Mode().IsRegular():
		file, err := os.Open(resolvedSourcePath)
		if err != nil {
			return inspectedManagedAgentMountSource{}, fmt.Errorf("open %q: %w", resolvedSourcePath, err)
		}
		if err := file.Close(); err != nil {
			return inspectedManagedAgentMountSource{}, fmt.Errorf("close %q: %w", resolvedSourcePath, err)
		}
		return inspectedManagedAgentMountSource{
			Kind:         managedAgentMountKindFile,
			ResolvedPath: resolvedSourcePath,
		}, nil
	case info.IsDir():
		if err := validateManagedAgentMountDirectorySource(resolvedSourcePath); err != nil {
			return inspectedManagedAgentMountSource{}, err
		}
		return inspectedManagedAgentMountSource{
			Kind:         managedAgentMountKindDirectory,
			ResolvedPath: resolvedSourcePath,
		}, nil
	default:
		return inspectedManagedAgentMountSource{}, fmt.Errorf("%q is %s, want regular file or directory", resolvedSourcePath, mountSourceFileKind(info.Mode()))
	}
}

func validateManagedAgentMountDirectorySource(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		switch {
		case entry.IsDir():
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
		return nil
	})
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
	if mount.isPathSource() {
		if strings.TrimSpace(mount.Source.ResolvedPath) == "" {
			return nil, fmt.Errorf("path-backed mount %q is missing source.resolved_path; re-sync or recreate the agent", mount.Path)
		}
		return nil, fmt.Errorf("path-backed mount %q does not store embedded content", mount.Path)
	}
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
	if strings.TrimSpace(mount.ContentBase64) == "" {
		return nil, fmt.Errorf("mount %q has no embedded content", mount.Path)
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

func (mount managedAgentMount) isPathSource() bool {
	return strings.TrimSpace(mount.Source.Path) != ""
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
		switch {
		case mount.isPathSource():
			if err := materializeAgentMountPathSource(fsys, targetPath, mount); err != nil {
				return fmt.Errorf("write mount %q: %w", targetPath, err)
			}
		case mount.kind() == managedAgentMountKindFile:
			content, err := mount.contentBytesWithLookup(lookupEnv)
			if err != nil {
				return err
			}
			if err := materializeAgentMountFile(fsys, targetPath, content); err != nil {
				return fmt.Errorf("write mount %q: %w", targetPath, err)
			}
		default:
			return fmt.Errorf("mount %q has unsupported kind %q", mount.Path, mount.Kind)
		}
	}
	return nil
}

func materializeAgentMountPathSource(fsys sandbox.FileSystem, targetPath string, mount managedAgentMount) error {
	resolvedSourcePath := strings.TrimSpace(mount.Source.ResolvedPath)
	if resolvedSourcePath == "" {
		return fmt.Errorf("path-backed mount %q is missing source.resolved_path; re-sync or recreate the agent", mount.Path)
	}
	inspectedSource, err := inspectManagedAgentMountResolvedSourcePath(resolvedSourcePath)
	if err != nil {
		return err
	}
	if inspectedSource.Kind != mount.kind() {
		return fmt.Errorf(
			"mount source %q changed from %q to %q",
			inspectedSource.ResolvedPath,
			mount.kind(),
			inspectedSource.Kind,
		)
	}
	switch inspectedSource.Kind {
	case managedAgentMountKindFile:
		return materializeAgentMountFileFromSourcePath(fsys, targetPath, inspectedSource.ResolvedPath)
	case managedAgentMountKindDirectory:
		return materializeAgentMountDirectoryFromSourcePath(fsys, targetPath, inspectedSource.ResolvedPath)
	default:
		return fmt.Errorf("mount %q has unsupported kind %q", mount.Path, inspectedSource.Kind)
	}
}

func materializeAgentMountDirectoryFromSourcePath(fsys sandbox.FileSystem, targetPath, sourceRoot string) error {
	if err := ensureMountDirectory(fsys, targetPath); err != nil {
		return err
	}
	return filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == sourceRoot {
			return nil
		}
		relPath, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		entryPath, err := sandbox.ResolvePath(fsys, targetPath, relPath)
		if err != nil {
			return fmt.Errorf("resolve directory mount entry %q: %w", relPath, err)
		}
		switch {
		case entry.IsDir():
			return ensureMountDirectory(fsys, entryPath)
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
		return materializeAgentMountFileFromSourcePath(fsys, entryPath, path)
	})
}

func materializeAgentMountFileFromSourcePath(fsys sandbox.FileSystem, targetPath, sourcePath string) error {
	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open %q: %w", sourcePath, err)
	}
	defer sourceFile.Close()
	return materializeAgentMountFileFromReader(fsys, targetPath, sourceFile)
}

func materializeAgentMountFileFromReader(fsys sandbox.FileSystem, targetPath string, content io.Reader) (err error) {
	if err := ensureMountDirectory(fsys, filepath.Dir(targetPath)); err != nil {
		return err
	}
	if err := ensureMountFileTarget(fsys, targetPath); err != nil {
		return err
	}
	targetFile, err := fsys.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := targetFile.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	if _, err = io.Copy(targetFile, content); err != nil {
		return err
	}
	return nil
}

func materializeAgentMountFile(fsys sandbox.FileSystem, targetPath string, content []byte) error {
	return materializeAgentMountFileFromReader(fsys, targetPath, bytes.NewReader(content))
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
