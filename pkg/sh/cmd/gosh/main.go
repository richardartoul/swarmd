// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

// gosh is a proof of concept shell built on top of [interp].
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
)

var command = flag.String("c", "", "command to be executed")

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
	return shellmain.RunAll(
		*command,
		flag.Args(),
		os.Stdin,
		os.Stdout,
		os.Stderr,
		term.IsTerminal(int(os.Stdin.Fd())),
		func(stdin io.Reader, stdout, stderr io.Writer) (*interp.Runner, error) {
			return interp.New(interp.Interactive(true), interp.StdIO(stdin, stdout, stderr))
		},
		nil,
		func(_ *interp.Runner, path string) (io.ReadCloser, string, error) {
			f, err := interp.OSFileSystem{}.Open(path)
			if err != nil {
				return nil, "", err
			}
			return f, path, nil
		},
	)
}

func run(r *interp.Runner, reader io.Reader, name string) error {
	return shellmain.Run(r, nil, reader, name)
}

func runPath(r *interp.Runner, path string) error {
	return shellmain.RunPath(r, path, nil, func(_ *interp.Runner, path string) (io.ReadCloser, string, error) {
		f, err := interp.OSFileSystem{}.Open(path)
		if err != nil {
			return nil, "", err
		}
		return f, path, nil
	})
}

func runInteractive(r *interp.Runner, stdin io.Reader, stdout, stderr io.Writer) error {
	_ = stderr
	return shellmain.RunInteractive(r, nil, stdin, stdout)
}
