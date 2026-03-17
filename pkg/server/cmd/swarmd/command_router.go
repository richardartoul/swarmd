package main

import (
	"context"
	"fmt"
	"strings"
)

type commandFunc func(context.Context, []string, commandIO) error

type commandSpec struct {
	name    string
	handler commandFunc
}

var rootCommands = []commandSpec{
	{name: "init", handler: runInitCommand},
	{name: "server", handler: runServer},
	{name: "config", handler: runConfigCommand},
	{name: "sync", handler: runSyncCommand},
	{name: "namespaces", handler: runNamespacesCommand},
	{name: "agents", handler: runAgentsCommand},
	{name: "agent", handler: runAgentCommand},
	{name: "mailbox", handler: runMailboxCommand},
	{name: "runs", handler: runRunsCommand},
	{name: "run", handler: runRunCommand},
	{name: "steps", handler: runStepsCommand},
	{name: "schedules", handler: runSchedulesCommand},
	{name: "thread", handler: runThreadCommand},
	{name: "tui", handler: runTUICommand},
}

func dispatchCommands(ctx context.Context, group string, args []string, streams commandIO, commands []commandSpec) error {
	if len(args) == 0 {
		if group == "" {
			printUsage(streams.stderr)
			return fmt.Errorf("expected subcommand")
		}
		return fmt.Errorf("%s requires a subcommand: %s", group, formatCommandAlternatives(commands))
	}
	name := strings.TrimSpace(args[0])
	for _, command := range commands {
		if command.name == name {
			return command.handler(ctx, args[1:], streams)
		}
	}
	if group == "" {
		printUsage(streams.stderr)
		return fmt.Errorf("unknown subcommand %q", name)
	}
	return fmt.Errorf("unknown %s subcommand %q", group, name)
}

func formatCommandAlternatives(commands []commandSpec) string {
	names := make([]string, 0, len(commands))
	for _, command := range commands {
		if strings.TrimSpace(command.name) != "" {
			names = append(names, command.name)
		}
	}
	switch len(names) {
	case 0:
		return "none"
	case 1:
		return names[0]
	case 2:
		return names[0] + " or " + names[1]
	default:
		return strings.Join(names[:len(names)-1], ", ") + ", or " + names[len(names)-1]
	}
}
