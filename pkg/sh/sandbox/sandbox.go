// See LICENSE for licensing information

// Package sandbox provides a best-effort in-process shell sandbox.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/richardartoul/swarmd/pkg/sh/expand"
	"github.com/richardartoul/swarmd/pkg/sh/interp"
	"github.com/richardartoul/swarmd/pkg/sh/moreinterp/coreutils"
	"github.com/richardartoul/swarmd/pkg/sh/syntax"
)

// FS is a root-constrained filesystem used by the sandboxed runner.
type FS struct {
	root string
}

// FileSystem is the filesystem contract required by sandbox runners.
type FileSystem interface {
	interp.RunnerFileSystem
	interp.MutableFileSystem
	interp.WorkingDirFileSystem
}

type pathResolver interface {
	ResolvePath(dir, path string) (string, error)
}

var _ interp.RunnerFileSystem = (*FS)(nil)
var _ interp.MutableFileSystem = (*FS)(nil)
var _ interp.WorkingDirFileSystem = (*FS)(nil)
var _ interp.ReadlinkFileSystem = (*FS)(nil)
var _ interp.RenameFileSystem = (*FS)(nil)
var _ interp.ChmodFileSystem = (*FS)(nil)
var _ interp.TempDirFileSystem = (*FS)(nil)
var _ interp.HostPathFileSystem = (*FS)(nil)

// NewFS constructs a root-constrained filesystem policy.
func NewFS(root string) (*FS, error) {
	if root == "" {
		return nil, fmt.Errorf("sandbox root must not be empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("could not get absolute sandbox root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("could not resolve sandbox root: %w", err)
	}
	if err == nil {
		abs = resolved
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("could not stat sandbox root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("sandbox root %q is not a directory", abs)
	}
	return &FS{root: filepath.Clean(abs)}, nil
}

// Root returns the configured sandbox root.
func (s *FS) Root() string {
	return s.root
}

func (s *FS) Getwd() (string, error) {
	return s.root, nil
}

func (s *FS) TempDir() string {
	return filepath.Join(s.root, ".tmp")
}

// Abs resolves path relative to the sandbox root and rejects lexical escapes.
func (s *FS) Abs(path string) (string, error) {
	return s.absFrom(s.root, path)
}

func (s *FS) ResolvePath(dir, path string) (string, error) {
	return s.absFrom(dir, path)
}

func (s *FS) HostPath(path string) (string, error) {
	return s.MapPath(path)
}

// RunnerConfig customizes how [NewRunnerWithConfig] wires a sandboxed interpreter.
type RunnerConfig struct {
	Stdin             io.Reader
	Stdout            io.Writer
	Stderr            io.Writer
	NetworkDialer     interp.NetworkDialer
	HTTPHeaders       []interp.HTTPHeaderRule
	HTTPClientFactory interp.HTTPClientFactory
	// DisableGlobbing preserves literal wildcard arguments by enabling the
	// shell's "noglob" mode. The default leaves pathname expansion enabled for
	// better POSIX shell compatibility.
	DisableGlobbing bool
	// NetworkEnabled controls whether network-requiring sandbox commands such as
	// curl are exposed via discovery and prompt surfaces.
	NetworkEnabled bool
	// ProgramValidator is applied to synthetic nested shell commands like `sh -c`
	// after they are parsed but before they are executed.
	ProgramValidator func(syntax.Node) error
	CustomCommands   []CustomCommand
	ExecHandlers     []func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc
	CallHandlers     []interp.CallHandlerFunc
}

// NewRunner creates a runner configured to use a sandbox filesystem policy.
func NewRunner(policy FileSystem, stdin io.Reader, stdout, stderr io.Writer) (*interp.Runner, error) {
	return NewRunnerWithConfig(policy, RunnerConfig{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
	})
}

func sandboxBaseEnvPairs(policy FileSystem, pwd string) ([]string, error) {
	homeDir, err := policy.Getwd()
	if err != nil {
		return nil, fmt.Errorf("resolve sandbox home dir: %w", err)
	}
	return []string{
		"HOME=" + homeDir,
		"PWD=" + pwd,
		"PATH=/__sandbox_external_commands_blocked__",
		"TMPDIR=" + sandboxTempDir(policy, homeDir),
		"UID=0",
		"EUID=0",
		"GID=0",
	}, nil
}

// NewRunnerWithConfig creates a runner configured to use a sandbox filesystem policy.
func NewRunnerWithConfig(policy FileSystem, cfg RunnerConfig) (*interp.Runner, error) {
	homeDir, err := policy.Getwd()
	if err != nil {
		return nil, fmt.Errorf("resolve sandbox home dir: %w", err)
	}
	tmpDir := sandboxTempDir(policy, homeDir)
	if err := policy.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating sandbox tmp dir: %w", err)
	}
	envPairs, err := sandboxBaseEnvPairs(policy, homeDir)
	if err != nil {
		return nil, err
	}
	env := expand.ListEnviron(envPairs...)
	runtimeCfg := sandboxRuntimeConfig{
		networkEnabled:   cfg.NetworkEnabled || cfg.NetworkDialer != nil,
		programValidator: cfg.ProgramValidator,
		customCommands:   append([]CustomCommand(nil), cfg.CustomCommands...),
		execHandlers:     append([]func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc(nil), cfg.ExecHandlers...),
		callHandlers:     append([]interp.CallHandlerFunc(nil), cfg.CallHandlers...),
	}
	httpClientFactory := cfg.HTTPClientFactory
	if httpClientFactory == nil {
		httpClientFactory, err = interp.NewHTTPClientFactory(cfg.NetworkDialer, cfg.HTTPHeaders)
		if err != nil {
			return nil, fmt.Errorf("creating sandbox HTTP client factory: %w", err)
		}
	}

	execHandlers := make([]func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc, 0, len(cfg.ExecHandlers)+4)
	execHandlers = append(execHandlers, cfg.ExecHandlers...)
	if len(cfg.CustomCommands) > 0 {
		execHandlers = append(execHandlers, customCommandExecHandler(cfg.CustomCommands))
	}
	execHandlers = append(execHandlers, sandboxExecHandler(runtimeCfg), coreutils.ExecHandler, blockExternalExec)

	callHandlers := make([]interp.CallHandlerFunc, 0, len(cfg.CallHandlers)+1)
	callHandlers = append(callHandlers, cfg.CallHandlers...)
	callHandlers = append(callHandlers, blockUnsafeBuiltins)

	opts := []interp.RunnerOption{
		interp.FileSystem(policy),
		interp.Env(env),
		interp.Dir(homeDir),
		interp.ShellVariant(syntax.LangPOSIX),
		interp.StdIO(cfg.Stdin, cfg.Stdout, cfg.Stderr),
		interp.ExecHandlers(execHandlers...),
		interp.CallHandler(chainCallHandlers(callHandlers...)),
		interp.HTTPClient(httpClientFactory),
	}
	if cfg.DisableGlobbing {
		opts = append(opts, interp.Params("-f"))
	}
	if cfg.NetworkDialer != nil {
		opts = append(opts, interp.Network(cfg.NetworkDialer))
	}
	return interp.New(opts...)
}

func sandboxTempDir(policy FileSystem, homeDir string) string {
	if policyWithTempDir, ok := policy.(interp.TempDirFileSystem); ok {
		return policyWithTempDir.TempDir()
	}
	return filepath.Join(homeDir, ".tmp")
}

func chainCallHandlers(handlers ...interp.CallHandlerFunc) interp.CallHandlerFunc {
	return func(ctx context.Context, args []string) ([]string, error) {
		for _, handler := range handlers {
			if handler == nil {
				continue
			}
			var err error
			args, err = handler(ctx, args)
			if err != nil {
				return nil, err
			}
			if len(args) == 0 {
				return nil, fmt.Errorf("sandbox call handler returned empty args")
			}
		}
		return args, nil
	}
}

func blockExternalExec(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		hc := interp.HandlerCtx(ctx)
		fmt.Fprintf(hc.Stderr, "%s: external commands disabled in sandbox\n", args[0])
		return interp.ExitStatus(126)
	}
}

var allowedBuiltins = map[string]struct{}{
	":":        {},
	"[":        {},
	"break":    {},
	"continue": {},
	"true":     {},
	"false":    {},
	"echo":     {},
	"printf":   {},
	"pwd":      {},
	"cd":       {},
	"set":      {},
	"shift":    {},
	"test":     {},
	"unset":    {},
	"exit":     {},
}

func blockUnsafeBuiltins(ctx context.Context, args []string) ([]string, error) {
	name := args[0]
	hc := interp.HandlerCtx(ctx)
	if name == "command" && hc.IsBuiltin(name) {
		return rewriteSandboxCommand(args), nil
	}
	if !hc.IsBuiltin(name) {
		return args, nil
	}
	if _, ok := allowedBuiltins[name]; ok {
		return args, nil
	}
	fmt.Fprintf(hc.Stderr, "%s: builtin disabled in sandbox\n", name)
	return []string{"false"}, nil
}

func (s *FS) absFrom(dir, path string) (string, error) {
	switch {
	case path == "":
		path = dir
	case !filepath.IsAbs(path):
		path = filepath.Join(dir, path)
	}
	path = filepath.Clean(path)
	if !isWithinRoot(s.root, path) {
		return "", pathError("sandbox", path)
	}
	return path, nil
}

// ResolvePath resolves a sandbox-visible path against dir using any filesystem-specific
// path restrictions the configured filesystem exposes.
func ResolvePath(fsys interp.RunnerFileSystem, dir, path string) (string, error) {
	if resolver, ok := fsys.(pathResolver); ok {
		return resolver.ResolvePath(dir, path)
	}
	switch {
	case path == "":
		path = dir
	case dir != "" && !filepath.IsAbs(path):
		path = filepath.Join(dir, path)
	}
	return filepath.Clean(path), nil
}

func (s *FS) MapPath(path string) (string, error) {
	if interp.IsDevNullPath(path) {
		return path, nil
	}
	if !isRootedPath(path) {
		return path, nil
	}
	original := path
	path = filepath.Clean(path)
	if isWithinRoot(s.root, path) {
		if err := s.checkPath(path, false); err != nil {
			return "", pathError("sandbox", original)
		}
		return path, nil
	}
	if vol := filepath.VolumeName(path); vol != "" {
		path = strings.TrimPrefix(path, vol)
	}
	rel := strings.TrimLeft(path, `/\`)
	mapped := filepath.Join(s.root, filepath.FromSlash(rel))
	if err := s.checkPath(mapped, false); err != nil {
		return "", pathError("sandbox", original)
	}
	return mapped, nil
}

func firstExistingPath(path string) (string, error) {
	path = filepath.Clean(path)
	for {
		_, err := os.Lstat(path)
		if err == nil {
			return path, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", err
		}
		path = parent
	}
}

func (s *FS) checkPath(path string, followFinal bool) error {
	if !isWithinRoot(s.root, path) {
		return pathError("sandbox", path)
	}
	existingPath, err := firstExistingPath(path)
	if err != nil {
		return err
	}
	resolvedPrefix, err := filepath.EvalSymlinks(existingPath)
	if err != nil {
		return err
	}
	if !isWithinRoot(s.root, resolvedPrefix) {
		return pathError("sandbox", path)
	}
	if !followFinal {
		return nil
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err == nil {
		if !isWithinRoot(s.root, resolvedPath) {
			return pathError("sandbox", path)
		}
		return nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
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

func pathError(op, path string) error {
	return &os.PathError{Op: op, Path: path, Err: fs.ErrPermission}
}

func (s *FS) Open(path string) (fs.File, error) {
	if interp.IsDevNullPath(path) {
		return interp.OSFileSystem{}.Open(path)
	}
	absPath, err := s.absFrom(s.root, path)
	if err != nil {
		return nil, err
	}
	if err := s.checkPath(absPath, true); err != nil {
		return nil, pathError("open", absPath)
	}
	return os.Open(absPath)
}

func (s *FS) OpenFile(path string, flag int, perm os.FileMode) (io.ReadWriteCloser, error) {
	if interp.IsDevNullPath(path) {
		return interp.OSFileSystem{}.OpenFile(path, flag, perm)
	}
	absPath, err := s.absFrom(s.root, path)
	if err != nil {
		return nil, err
	}
	if err := s.checkPath(absPath, true); err != nil {
		return nil, pathError("open", absPath)
	}
	return os.OpenFile(absPath, flag, perm)
}

func (s *FS) ReadDir(path string) ([]fs.DirEntry, error) {
	absPath, err := s.absFrom(s.root, path)
	if err != nil {
		return nil, err
	}
	if err := s.checkPath(absPath, true); err != nil {
		return nil, pathError("readdir", absPath)
	}
	return os.ReadDir(absPath)
}

func (s *FS) Stat(path string) (fs.FileInfo, error) {
	if interp.IsDevNullPath(path) {
		return interp.OSFileSystem{}.Stat(path)
	}
	absPath, err := s.absFrom(s.root, path)
	if err != nil {
		return nil, err
	}
	if err := s.checkPath(absPath, true); err != nil {
		return nil, pathError("stat", absPath)
	}
	return os.Stat(absPath)
}

func (s *FS) Lstat(path string) (fs.FileInfo, error) {
	if interp.IsDevNullPath(path) {
		return interp.OSFileSystem{}.Lstat(path)
	}
	absPath, err := s.absFrom(s.root, path)
	if err != nil {
		return nil, err
	}
	if err := s.checkPath(absPath, false); err != nil {
		return nil, pathError("stat", absPath)
	}
	return os.Lstat(absPath)
}

func (s *FS) Remove(path string) error {
	absPath, err := s.absFrom(s.root, path)
	if err != nil {
		return err
	}
	if err := s.checkPath(absPath, false); err != nil {
		return pathError("remove", absPath)
	}
	return os.Remove(absPath)
}

func (s *FS) Readlink(path string) (string, error) {
	absPath, err := s.absFrom(s.root, path)
	if err != nil {
		return "", err
	}
	if err := s.checkPath(absPath, false); err != nil {
		return "", pathError("readlink", absPath)
	}
	return os.Readlink(absPath)
}

func (s *FS) Rename(oldpath, newpath string) error {
	absOldPath, err := s.absFrom(s.root, oldpath)
	if err != nil {
		return err
	}
	if err := s.checkPath(absOldPath, false); err != nil {
		return pathError("rename", absOldPath)
	}
	absNewPath, err := s.absFrom(s.root, newpath)
	if err != nil {
		return err
	}
	if err := s.checkPath(absNewPath, false); err != nil {
		return pathError("rename", absNewPath)
	}
	return os.Rename(absOldPath, absNewPath)
}

func (s *FS) Chmod(path string, mode os.FileMode) error {
	absPath, err := s.absFrom(s.root, path)
	if err != nil {
		return err
	}
	if err := s.checkPath(absPath, true); err != nil {
		return pathError("chmod", absPath)
	}
	return os.Chmod(absPath, mode)
}

func (s *FS) Mkdir(path string, perm os.FileMode) error {
	absPath, err := s.absFrom(s.root, path)
	if err != nil {
		return err
	}
	if err := s.checkPath(absPath, false); err != nil {
		return pathError("mkdir", absPath)
	}
	return os.Mkdir(absPath, perm)
}

func (s *FS) MkdirAll(path string, perm os.FileMode) error {
	absPath, err := s.absFrom(s.root, path)
	if err != nil {
		return err
	}
	if err := s.checkPath(absPath, false); err != nil {
		return pathError("mkdir", absPath)
	}
	return os.MkdirAll(absPath, perm)
}

func (s *FS) RemoveAll(path string) error {
	absPath, err := s.absFrom(s.root, path)
	if err != nil {
		return err
	}
	if err := s.checkPath(absPath, false); err != nil {
		return pathError("remove", absPath)
	}
	return os.RemoveAll(absPath)
}

func (s *FS) WriteFile(path string, data []byte, perm os.FileMode) error {
	absPath, err := s.absFrom(s.root, path)
	if err != nil {
		return err
	}
	if err := s.checkPath(absPath, true); err != nil {
		return pathError("write", absPath)
	}
	return os.WriteFile(absPath, data, perm)
}

func (s *FS) Link(oldname, newname string) error {
	absOldPath, err := s.absFrom(s.root, oldname)
	if err != nil {
		return err
	}
	// os.Link follows a symlink source to its target, so the source needs the
	// same final-component resolution check as reads do.
	if err := s.checkPath(absOldPath, true); err != nil {
		return pathError("link", absOldPath)
	}
	absNewPath, err := s.absFrom(s.root, newname)
	if err != nil {
		return err
	}
	if err := s.checkPath(absNewPath, false); err != nil {
		return pathError("link", absNewPath)
	}
	return os.Link(absOldPath, absNewPath)
}

func (s *FS) Symlink(oldname, newname string) error {
	absNewPath, err := s.absFrom(s.root, newname)
	if err != nil {
		return err
	}
	if err := s.checkPath(absNewPath, false); err != nil {
		return pathError("symlink", absNewPath)
	}
	if err := s.checkSymlinkTarget(absNewPath, oldname); err != nil {
		return err
	}
	return os.Symlink(oldname, absNewPath)
}

func (s *FS) Chtimes(path string, atime, mtime time.Time) error {
	absPath, err := s.absFrom(s.root, path)
	if err != nil {
		return err
	}
	if err := s.checkPath(absPath, true); err != nil {
		return pathError("chtimes", absPath)
	}
	return os.Chtimes(absPath, atime, mtime)
}

func (s *FS) checkSymlinkTarget(linkPath, target string) error {
	if target == "" {
		return nil
	}
	checkPath := target
	if filepath.IsAbs(target) {
		if vol := filepath.VolumeName(target); vol != "" {
			checkPath = strings.TrimPrefix(target, vol)
		}
		rel := strings.TrimLeft(checkPath, `/\`)
		checkPath = filepath.Join(s.root, filepath.FromSlash(rel))
	} else {
		checkPath = filepath.Join(filepath.Dir(linkPath), target)
	}
	checkPath = filepath.Clean(checkPath)
	if !isWithinRoot(s.root, checkPath) {
		return pathError("symlink", target)
	}
	return nil
}
