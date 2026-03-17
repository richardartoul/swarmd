package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"

	_ "github.com/richardartoul/swarmd/pkg/tools/customtools"
)

const (
	defaultConfigRoot     = "./server-config"
	defaultDataDir        = "./data"
	defaultSQLiteFilename = "swarmd-server.db"
	defaultAgentRootsDir  = "agents"
)

var (
	defaultSQLitePath = defaultSQLitePathForDataDir(defaultDataDir)
	defaultRootBase   = defaultRootBaseForDataDir(defaultDataDir)
)

type commandIO struct {
	stdout io.Writer
	stderr io.Writer
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if err := run(ctx, os.Args[1:], commandIO{
		stdout: os.Stdout,
		stderr: os.Stderr,
	}); err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, streams commandIO) error {
	if len(args) > 0 {
		switch args[0] {
		case "help", "-h", "--help":
			printUsage(streams.stdout)
			return nil
		}
	}
	return dispatchCommands(ctx, "", args, streams, rootCommands)
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  swarmd <subcommand> [flags]

Subcommands:
  init              Scaffold a config root with a default heartbeat agent.
  server            Load YAML agent specs into SQLite and run the multi-namespace worker runtime.
  config validate   Validate YAML agent specs without touching SQLite.
  sync plan         Preview config-vs-SQLite drift without mutating state.
  namespaces ls     List namespaces and mailbox/runtime counts.
  agents ls         List agents, optionally scoped to a namespace.
  agent show        Show one agent, its config, prompt, and schedules.
  mailbox ls        List mailbox messages with status and retry info.
  mailbox show      Show one mailbox message with decoded payload/metadata.
  runs ls           List runs with status, duration, and final error.
  run show          Show one run and all recorded shell steps.
  steps ls          List steps for a run.
  schedules ls      List schedules and next/last fire times.
  thread show       Show durable thread history with decoded payloads.
  tui               Open an interactive read-only SQLite explorer.
`)
}
