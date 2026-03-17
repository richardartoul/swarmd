// Copyright (c) 2018, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/richardartoul/swarmd/pkg/sh/expand"
	"github.com/richardartoul/swarmd/pkg/sh/interp"
	"github.com/richardartoul/swarmd/pkg/sh/syntax"
)

func Example() {
	src := `
		foo=abc
		for i in 1 2 3; do
			foo+=$i
		done
		let bar=(2 + 3)
		echo $foo $bar
		echo $GLOBAL
	`
	file, _ := syntax.NewParser().Parse(strings.NewReader(src), "")
	runner, _ := interp.New(
		// Use [interp.Interactive] to enable interactive shell defaults like expanding aliases.
		interp.Env(expand.ListEnviron("GLOBAL=global_value")),
		interp.StdIO(nil, os.Stdout, os.Stdout),
	)
	runner.Run(context.TODO(), file)
	// Output:
	// abc123 5
	// global_value
}

func ExampleExecHandlers() {
	src := "echo foo; join ! foo bar baz; missing-program bar"
	file, _ := syntax.NewParser().Parse(strings.NewReader(src), "")
	fsys := interp.DefaultRunnerFileSystem()

	execJoin := func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			hc := interp.HandlerCtx(ctx)
			if args[0] == "join" {
				fmt.Fprintln(hc.Stdout, strings.Join(args[2:], args[1]))
				return nil
			}
			return next(ctx, args)
		}
	}
	execNotInstalled := func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			hc := interp.HandlerCtx(ctx)
			if _, err := interp.LookPathDir(fsys, hc.Dir, hc.Env, args[0]); err != nil {
				fmt.Printf("%s is not installed\n", args[0])
				return interp.ExitStatus(1)
			}
			return next(ctx, args)
		}
	}
	runner, _ := interp.New(
		interp.FileSystem(fsys),
		interp.StdIO(nil, os.Stdout, os.Stdout),
		interp.ExecHandlers(execJoin, execNotInstalled),
	)
	runner.Run(context.TODO(), file)
	// Output:
	// foo
	// foo!bar!baz
	// missing-program is not installed
}

type nopWriterCloser struct {
	*strings.Reader
}

func (nopWriterCloser) Write([]byte) (int, error) { return 0, io.EOF }
func (nopWriterCloser) Close() error              { return nil }

type exampleFS struct{ interp.OSFileSystem }

func (f exampleFS) OpenFile(path string, flag int, perm os.FileMode) (io.ReadWriteCloser, error) {
	// Hard-code the contents of the hostname file for all platforms.
	if path == "/etc/hostname" {
		return nopWriterCloser{strings.NewReader("mymachine")}, nil
	}
	// OSFileSystem redirects /dev/null to NUL on Windows.
	return f.OSFileSystem.OpenFile(path, flag, perm)
}

func ExampleFileSystem() {
	src := "echo $(</etc/hostname); echo bar >/dev/null"
	file, _ := syntax.NewParser().Parse(strings.NewReader(src), "")
	runner, _ := interp.New(
		interp.StdIO(nil, os.Stdout, os.Stdout),
		interp.FileSystem(exampleFS{}),
	)
	runner.Run(context.TODO(), file)
	// Output:
	// mymachine
}
