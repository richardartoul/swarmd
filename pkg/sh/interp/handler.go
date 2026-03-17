// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/richardartoul/swarmd/pkg/sh/expand"
	"github.com/richardartoul/swarmd/pkg/sh/syntax"
)

// HandlerCtx returns the [HandlerContext] value stored in ctx,
// which is used when calling handler functions.
// It panics if ctx has no HandlerContext stored.
func HandlerCtx(ctx context.Context) HandlerContext {
	hc, ok := ctx.Value(handlerCtxKey{}).(HandlerContext)
	if !ok {
		panic("interp.HandlerCtx: no HandlerContext in ctx")
	}
	return hc
}

type handlerCtxKey struct{}

type handlerKind int

const (
	_               handlerKind = iota
	handlerKindExec             // [ExecHandlerFunc]
	handlerKindCall             // [CallHandlerFunc]
)

// HandlerContext is the data passed to all the handler functions via [context.WithValue].
// It contains some of the current state of the [Runner].
type HandlerContext struct {
	runner *Runner // for internal use only, e.g. [HandlerContext.Builtin]

	// kind records which type of handler this context was built for.
	kind handlerKind

	// Env is a read-only version of the interpreter's environment,
	// including environment variables, global variables, and local function
	// variables.
	Env expand.Environ

	// Dir is the interpreter's current directory.
	Dir string

	// FileSystem is the filesystem adapter configured via [FileSystem].
	FileSystem RunnerFileSystem

	// NetworkDialer is the network adapter configured via [Network].
	NetworkDialer NetworkDialer

	// HTTPClientFactory constructs HTTP clients for interpreter-owned commands
	// such as curl.
	HTTPClientFactory HTTPClientFactory

	// Pos is the source position which relates to the operation,
	// such as a [syntax.CallExpr] when calling an [ExecHandlerFunc].
	// It may be invalid if the operation has no relevant position information.
	Pos syntax.Pos

	// TODO(v4): use an os.File for stdin below directly.

	// Stdin is the interpreter's current standard input reader.
	// It is always an [*os.File], but the type here remains an [io.Reader]
	// due to backwards compatibility.
	Stdin io.Reader
	// Stdout is the interpreter's current standard output writer.
	Stdout io.Writer
	// Stderr is the interpreter's current standard error writer.
	Stderr io.Writer
}

// IsBuiltin reports whether a name is an enabled builtin in the current
// runner's shell mode.
func (hc HandlerContext) IsBuiltin(name string) bool {
	return hc.runner.isBuiltin(name)
}

// IsFunction reports whether a name resolves to a shell function in the current runner.
func (hc HandlerContext) IsFunction(name string) bool {
	return hc.runner.Funcs[name] != nil
}

// RunSimpleCommandOptions configures how [HandlerContext.RunSimpleCommand] resolves a command.
type RunSimpleCommandOptions struct {
	// SuppressFunctions skips shell function lookup, matching `command name ...` semantics.
	SuppressFunctions bool
	// Env overrides the environment used while resolving and executing the nested command.
	Env expand.WriteEnviron
}

// CallHandlerFunc is a handler which runs on every [syntax.CallExpr].
// It is called once variable assignments and field expansion have occurred.
// The context includes a [HandlerContext] value.
//
// The call's arguments are replaced by what the handler returns,
// and then the call is executed by the Runner as usual.
// The args slice is never empty.
// At this time, returning an empty slice without an error is not supported.
//
// This handler is similar to [ExecHandlerFunc], but has two major differences:
//
// First, it runs for all simple commands, including function calls and builtins.
//
// Second, it is not expected to execute the simple command, but instead to
// allow running custom code which allows replacing the argument list.
// Shell builtins touch on many internals of the Runner, after all.
//
// Returning a non-nil error will halt the [Runner] and will be returned via the API.
type CallHandlerFunc func(ctx context.Context, args []string) ([]string, error)

// TODO: consistently treat handler errors as non-fatal by default,
// but have an interface or API to specify fatal errors which should make
// the shell exit with a particular status code.

// ExecHandlerFunc is a handler which executes simple commands.
// It is called for all [syntax.CallExpr] nodes
// where the first argument is neither a declared function nor a builtin.
// The args slice is never empty.
// The context includes a [HandlerContext] value.
//
// Returning a nil error means a zero exit status.
// Other exit statuses can be set by returning or wrapping a [NewExitStatus] error,
// and such an error is returned via the API if it is the last statement executed.
// Any other error will halt the [Runner] and will be returned via the API.
type ExecHandlerFunc func(ctx context.Context, args []string) error

// DefaultExecHandler returns the [ExecHandlerFunc] used by default.
// It finds binaries in PATH and executes them.
// When context is cancelled, an interrupt signal is sent to running processes.
// killTimeout is a duration to wait before sending the kill signal.
// A negative value means that a kill signal will be sent immediately.
//
// On Windows, the kill signal is always sent immediately,
// because Go doesn't currently support sending Interrupt on Windows.
// [Runner] defaults to a killTimeout of 2 seconds.
func DefaultExecHandler(killTimeout time.Duration) ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		hc := HandlerCtx(ctx)
		path, err := LookPathDir(hc.runner.fileSystem, hc.Dir, hc.Env, args[0])
		if err != nil {
			fmt.Fprintln(hc.Stderr, err)
			return ExitStatus(127)
		}
		hostPath, err := fsHostPath(hc.runner.fileSystem, path)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "%s: external commands unavailable with current filesystem backend\n", args[0])
			return ExitStatus(126)
		}
		hostDir, err := fsHostPath(hc.runner.fileSystem, hc.Dir)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "%s: external commands unavailable with current filesystem backend\n", args[0])
			return ExitStatus(126)
		}
		cmd := exec.Cmd{
			Path:   hostPath,
			Args:   args,
			Env:    execEnv(hc.Env),
			Dir:    hostDir,
			Stdin:  hc.Stdin,
			Stdout: hc.Stdout,
			Stderr: hc.Stderr,
		}

		err = cmd.Start()
		if err == nil {
			stopf := context.AfterFunc(ctx, func() {
				if killTimeout <= 0 || runtime.GOOS == "windows" {
					_ = cmd.Process.Signal(os.Kill)
					return
				}
				_ = cmd.Process.Signal(os.Interrupt)
				// TODO: don't sleep in this goroutine if the program
				// stops itself with the interrupt above.
				time.Sleep(killTimeout)
				_ = cmd.Process.Signal(os.Kill)
			})
			defer stopf()

			err = cmd.Wait()
		}

		switch err := err.(type) {
		case *exec.ExitError:
			// Windows and Plan9 do not have support for [syscall.WaitStatus]
			// with methods like Signaled and Signal, so for those, [waitStatus] is a no-op.
			// Note: [waitStatus] is an alias [syscall.WaitStatus]
			if status, ok := err.Sys().(waitStatus); ok && status.Signaled() {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return ExitStatus(128 + status.Signal())
			}
			return ExitStatus(err.ExitCode())
		case *exec.Error:
			// did not start
			fmt.Fprintf(hc.Stderr, "%v\n", err)
			return ExitStatus(127)
		default:
			return err
		}
	}
}

func resolveStatPath(fsys fs.StatFS, dir, path string) (string, error) {
	if IsDevNullPath(path) {
		return path, nil
	}
	if resolver, ok := fsys.(ResolvePathFileSystem); ok {
		return resolver.ResolvePath(dir, path)
	}
	return resolvePath(dir, path), nil
}

func checkStat(fsys fs.StatFS, dir, file string, checkExec bool) (string, error) {
	var err error
	file, err = resolveStatPath(fsys, dir, file)
	if err != nil {
		return "", err
	}
	info, err := fsys.Stat(file)
	if err != nil {
		return "", err
	}
	m := info.Mode()
	if m.IsDir() {
		return "", fmt.Errorf("is a directory")
	}
	if checkExec && runtime.GOOS != "windows" && m&0o111 == 0 {
		return "", fmt.Errorf("permission denied")
	}
	return file, nil
}

func winHasExt(file string) bool {
	i := strings.LastIndex(file, ".")
	if i < 0 {
		return false
	}
	return strings.LastIndexAny(file, `:\/`) < i
}

// findExecutable returns the path to an existing executable file.
func findExecutable(fsys fs.StatFS, dir, file string, exts []string) (string, error) {
	if len(exts) == 0 {
		// non-windows
		return checkStat(fsys, dir, file, true)
	}
	if winHasExt(file) {
		if file, err := checkStat(fsys, dir, file, true); err == nil {
			return file, nil
		}
	}
	for _, e := range exts {
		f := file + e
		if f, err := checkStat(fsys, dir, f, true); err == nil {
			return f, nil
		}
	}
	return "", fmt.Errorf("not found")
}

// findFile returns the path to an existing file.
func findFile(fsys fs.StatFS, dir, file string, _ []string) (string, error) {
	return checkStat(fsys, dir, file, false)
}

// LookPath is deprecated; see [LookPathDir].
func LookPath(fsys fs.StatFS, env expand.Environ, file string) (string, error) {
	return LookPathDir(fsys, env.Get("PWD").String(), env, file)
}

// LookPathDir is similar to [os/exec.LookPath], with the difference that it uses the
// provided environment. env is used to fetch relevant environment variables
// such as PWD and PATH.
//
// If no error is returned, the returned path must be valid.
func LookPathDir(fsys fs.StatFS, cwd string, env expand.Environ, file string) (string, error) {
	return lookPathDir(fsys, cwd, env, file, findExecutable)
}

// findAny defines a function to pass to [lookPathDir].
type findAny = func(fsys fs.StatFS, dir string, file string, exts []string) (string, error)

func lookPathDir(fsys fs.StatFS, cwd string, env expand.Environ, file string, find findAny) (string, error) {
	if find == nil {
		panic("no find function found")
	}

	pathList := filepath.SplitList(env.Get("PATH").String())
	if len(pathList) == 0 {
		pathList = []string{""}
	}
	chars := `/`
	if runtime.GOOS == "windows" {
		chars = `:\/`
	}
	exts := pathExts(env)
	if strings.ContainsAny(file, chars) {
		return find(fsys, cwd, file, exts)
	}
	for _, elem := range pathList {
		var path string
		switch elem {
		case "", ".":
			// otherwise "foo" won't be "./foo"
			path = "." + string(filepath.Separator) + file
		default:
			path = filepath.Join(elem, file)
		}
		if f, err := find(fsys, cwd, path, exts); err == nil {
			return f, nil
		}
	}
	return "", fmt.Errorf("%q: executable file not found in $PATH", file)
}

// scriptFromPathDir is similar to [LookPathDir], with the difference that it looks
// for both executable and non-executable files.
func scriptFromPathDir(fsys fs.StatFS, cwd string, env expand.Environ, file string) (string, error) {
	return lookPathDir(fsys, cwd, env, file, findFile)
}

func pathExts(env expand.Environ) []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	pathext := env.Get("PATHEXT").String()
	if pathext == "" {
		return []string{".com", ".exe", ".bat", ".cmd"}
	}
	var exts []string
	for e := range strings.SplitSeq(strings.ToLower(pathext), `;`) {
		if e == "" {
			continue
		}
		if e[0] != '.' {
			e = "." + e
		}
		exts = append(exts, e)
	}
	return exts
}
