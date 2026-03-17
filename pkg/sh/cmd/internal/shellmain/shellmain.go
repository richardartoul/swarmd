package shellmain

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/richardartoul/swarmd/pkg/sh/interp"
	"github.com/richardartoul/swarmd/pkg/sh/syntax"
)

type RunnerFactory func(stdin io.Reader, stdout, stderr io.Writer) (*interp.Runner, error)

type ParserFactory func() *syntax.Parser

type OpenPathFunc func(r *interp.Runner, path string) (io.ReadCloser, string, error)

func RunAll(
	command string,
	args []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	interactive bool,
	newRunner RunnerFactory,
	newParser ParserFactory,
	openPath OpenPathFunc,
) error {
	r, err := newRunner(stdin, stdout, stderr)
	if err != nil {
		return err
	}
	if command != "" {
		return Run(r, newParser, strings.NewReader(command), "")
	}
	if len(args) == 0 {
		if interactive {
			return RunInteractive(r, newParser, stdin, stdout)
		}
		return Run(r, newParser, stdin, "")
	}
	for _, path := range args {
		if err := RunPath(r, path, newParser, openPath); err != nil {
			return err
		}
	}
	return nil
}

func Run(r *interp.Runner, newParser ParserFactory, reader io.Reader, name string) error {
	prog, err := parserFactory(newParser)().Parse(reader, name)
	if err != nil {
		return err
	}
	r.Reset()
	return r.Run(context.Background(), prog)
}

func RunPath(r *interp.Runner, path string, newParser ParserFactory, openPath OpenPathFunc) error {
	reader, name, err := openPath(r, path)
	if err != nil {
		return err
	}
	defer reader.Close()
	return Run(r, newParser, reader, name)
}

func RunInteractive(r *interp.Runner, newParser ParserFactory, stdin io.Reader, stdout io.Writer) error {
	parser := parserFactory(newParser)()
	fmt.Fprintf(stdout, "$ ")
	for stmts, err := range parser.InteractiveSeq(stdin) {
		if err != nil {
			return err
		}
		if parser.Incomplete() {
			fmt.Fprintf(stdout, "> ")
			continue
		}
		for _, stmt := range stmts {
			err := r.Run(context.Background(), stmt)
			if r.Exited() {
				return err
			}
		}
		fmt.Fprintf(stdout, "$ ")
	}
	return nil
}

func parserFactory(newParser ParserFactory) ParserFactory {
	if newParser != nil {
		return newParser
	}
	return func() *syntax.Parser {
		return syntax.NewParser()
	}
}
