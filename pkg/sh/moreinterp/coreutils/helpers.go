package coreutils

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/richardartoul/swarmd/pkg/sh/interp"
)

type commandFunc func(env *commandEnv, args []string) error

type commandEnv struct {
	ctx  context.Context
	hc   interp.HandlerContext
	next interp.ExecHandlerFunc
}

type resolvedPath struct {
	raw  string
	path string
}

func newCommandEnv(ctx context.Context, next interp.ExecHandlerFunc) *commandEnv {
	return &commandEnv{
		ctx:  ctx,
		hc:   interp.HandlerCtx(ctx),
		next: next,
	}
}

func (env *commandEnv) stdin() io.Reader {
	if env.hc.Stdin == nil {
		return strings.NewReader("")
	}
	return env.hc.Stdin
}

func (env *commandEnv) stdout() io.Writer {
	if env.hc.Stdout == nil {
		return io.Discard
	}
	return env.hc.Stdout
}

func (env *commandEnv) stderr() io.Writer {
	if env.hc.Stderr == nil {
		return io.Discard
	}
	return env.hc.Stderr
}

func usageError(env *commandEnv, status int, lines ...string) error {
	for _, line := range lines {
		if _, err := fmt.Fprintln(env.stderr(), line); err != nil {
			return err
		}
	}
	return interp.ExitStatus(status)
}

func (env *commandEnv) resolvePathArg(userPath string) (string, error) {
	if userPath == "" {
		return "", nil
	}
	if interp.IsDevNullPath(userPath) {
		return userPath, nil
	}
	return interp.ResolvePath(env.hc.FileSystem, env.hc.Dir, userPath)
}

func (env *commandEnv) resolvePaths(userPaths []string) ([]resolvedPath, error) {
	resolved := make([]resolvedPath, len(userPaths))
	for i, userPath := range userPaths {
		path, err := env.resolvePathArg(userPath)
		if err != nil {
			return nil, err
		}
		resolved[i] = resolvedPath{raw: userPath, path: path}
	}
	return resolved, nil
}

func (env *commandEnv) mutableFS() (interp.MutableFileSystem, error) {
	fsys, ok := env.hc.FileSystem.(interp.MutableFileSystem)
	if !ok {
		return nil, fmt.Errorf("filesystem does not support mutations")
	}
	return fsys, nil
}

func (env *commandEnv) readlinkFS() (interp.ReadlinkFileSystem, error) {
	fsys, ok := env.hc.FileSystem.(interp.ReadlinkFileSystem)
	if !ok {
		return nil, fmt.Errorf("filesystem does not support readlink")
	}
	return fsys, nil
}

func (env *commandEnv) renameFS() (interp.RenameFileSystem, error) {
	fsys, ok := env.hc.FileSystem.(interp.RenameFileSystem)
	if !ok {
		return nil, fmt.Errorf("filesystem does not support rename")
	}
	return fsys, nil
}

func (env *commandEnv) chmodFS() (interp.ChmodFileSystem, error) {
	fsys, ok := env.hc.FileSystem.(interp.ChmodFileSystem)
	if !ok {
		return nil, fmt.Errorf("filesystem does not support chmod")
	}
	return fsys, nil
}

func (env *commandEnv) dispatch(args []string) error {
	return env.dispatchWithOptions(args, interp.RunSimpleCommandOptions{})
}

func (env *commandEnv) dispatchWithOptions(args []string, opts interp.RunSimpleCommandOptions) error {
	if len(args) == 0 {
		return nil
	}
	return env.hc.RunSimpleCommandWithOptions(env.ctx, args, opts)
}

func walkResolved(fsys interp.RunnerFileSystem, root string, walkFn func(path string, info fs.FileInfo, err error) error) error {
	info, err := fsys.Lstat(root)
	if err != nil {
		return walkFn(root, nil, err)
	}
	return walkResolvedInfo(fsys, root, info, walkFn)
}

func walkResolvedInfo(fsys interp.RunnerFileSystem, currentPath string, info fs.FileInfo, walkFn func(path string, info fs.FileInfo, err error) error) error {
	if err := walkFn(currentPath, info, nil); err != nil {
		if err == filepath.SkipDir && info.IsDir() {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}

	entries, err := fsys.ReadDir(currentPath)
	if err != nil {
		if walkErr := walkFn(currentPath, info, err); walkErr == filepath.SkipDir {
			return nil
		} else {
			return walkErr
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		childPath := filepath.Join(currentPath, entry.Name())
		childInfo, err := fsys.Lstat(childPath)
		if err != nil {
			if walkErr := walkFn(childPath, nil, err); walkErr != nil && walkErr != filepath.SkipDir {
				return walkErr
			}
			continue
		}
		if err := walkResolvedInfo(fsys, childPath, childInfo, walkFn); err != nil {
			if err == filepath.SkipDir {
				continue
			}
			return err
		}
	}
	return nil
}

func sameFile(fsys interp.RunnerFileSystem, firstPath string, firstInfo fs.FileInfo, secondPath string, secondInfo fs.FileInfo) bool {
	if filepath.Clean(firstPath) == filepath.Clean(secondPath) {
		return true
	}
	return interp.SameFile(fsys, firstPath, firstInfo, secondPath, secondInfo)
}

func displayRootPath(raw string) string {
	if raw == "" {
		return "."
	}
	return raw
}

func joinDisplayPath(rootRaw, rel string) string {
	rootRaw = filepath.ToSlash(displayRootPath(rootRaw))
	rel = filepath.ToSlash(rel)
	if rel == "" || rel == "." {
		return rootRaw
	}
	if rootRaw == "." {
		return rel
	}
	return path.Join(rootRaw, rel)
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
