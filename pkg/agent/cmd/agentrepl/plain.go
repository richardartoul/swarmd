package main

import (
	"context"
	"fmt"
	"os"

	"golang.org/x/term"

	"github.com/richardartoul/swarmd/pkg/agent"
)

func runPlainCommand(ctx context.Context, opts runtimeOptions) error {
	driver := opts.baseDriver
	if opts.verbose {
		driver = verboseDriver{
			next:   driver,
			stdout: os.Stdout,
		}
	}

	stdinFile := os.Stdin
	queue := agent.Queue(newREPLQueue(stdinFile, os.Stdout))
	if opts.singlePrompt != "" {
		queue = &singlePromptQueue{
			trigger: makeTrigger(opts.singlePrompt, 1),
		}
	}

	if opts.singlePrompt == "" && term.IsTerminal(int(stdinFile.Fd())) {
		fmt.Fprintln(os.Stdout, "agentrepl ready. Enter a prompt to trigger the agent, or :quit to exit.")
	}

	printer := progressPrinter{
		stdout:  os.Stdout,
		stderr:  os.Stderr,
		verbose: opts.verbose,
	}

	cfg, err := opts.agentConfig(queue, driver, printer, printer, os.Stdout, os.Stderr)
	if err != nil {
		return err
	}
	runtime, err := agent.New(cfg)
	if err != nil {
		return err
	}
	return runtime.Serve(ctx)
}
