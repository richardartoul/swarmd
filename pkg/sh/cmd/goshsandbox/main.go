// See LICENSE for licensing information

// goshsandbox is a constrained shell built on top of [interp].
//
// It is intended to be a best-effort in-process sandbox:
// - all external command execution is blocked
// - filesystem access is confined to a root directory
// - parser runs in POSIX mode, disallowing Bash process substitution
//
// For high-assurance isolation, run this process inside an OS sandbox
// (container, VM, jail, seccomp profile, etc).
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"

	"github.com/richardartoul/swarmd/pkg/sh/cmd/internal/shellmain"
	"github.com/richardartoul/swarmd/pkg/sh/interp"
	"github.com/richardartoul/swarmd/pkg/sh/memfs"
	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
	"github.com/richardartoul/swarmd/pkg/sh/syntax"
)

var (
	command  = flag.String("c", "", "command to be executed")
	rootDir  = flag.String("root", ".", "sandbox root directory, or logical root when -memfs is set")
	useMemFS = flag.Bool("memfs", false, "use an in-memory filesystem backend")
)

func main() {
	flag.Parse()
	err := runAll()
	var es interp.ExitStatus
	if errors.As(err, &es) {
		os.Exit(int(es))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runAll() error {
	policy, err := newFileSystem()
	if err != nil {
		return err
	}
	return shellmain.RunAll(
		*command,
		flag.Args(),
		os.Stdin,
		os.Stdout,
		os.Stderr,
		term.IsTerminal(int(os.Stdin.Fd())),
		func(stdin io.Reader, stdout, stderr io.Writer) (*interp.Runner, error) {
			return sandbox.NewRunner(policy, stdin, stdout, stderr)
		},
		func() *syntax.Parser {
			return syntax.NewParser(syntax.Variant(syntax.LangPOSIX))
		},
		func(r *interp.Runner, path string) (io.ReadCloser, string, error) {
			absPath, err := interp.ResolvePath(policy, r.Dir, path)
			if err != nil {
				return nil, "", err
			}
			f, err := policy.Open(absPath)
			if err != nil {
				return nil, "", err
			}
			return f, absPath, nil
		},
	)
}

func newParser() *syntax.Parser {
	return syntax.NewParser(syntax.Variant(syntax.LangPOSIX))
}

func run(r *interp.Runner, reader io.Reader, name string) error {
	return shellmain.Run(r, newParser, reader, name)
}

func runPath(policy sandbox.FileSystem, r *interp.Runner, path string) error {
	return shellmain.RunPath(r, path, newParser, func(r *interp.Runner, path string) (io.ReadCloser, string, error) {
		absPath, err := interp.ResolvePath(policy, r.Dir, path)
		if err != nil {
			return nil, "", err
		}
		f, err := policy.Open(absPath)
		if err != nil {
			return nil, "", err
		}
		return f, absPath, nil
	})
}

func newFileSystem() (sandbox.FileSystem, error) {
	if *useMemFS {
		return memfs.New(*rootDir)
	}
	return sandbox.NewFS(*rootDir)
}

func runInteractive(r *interp.Runner, stdin io.Reader, stdout io.Writer) error {
	return shellmain.RunInteractive(r, newParser, stdin, stdout)
}
