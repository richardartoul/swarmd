package main

import "context"

var (
	configCommands = []commandSpec{
		{name: "validate", handler: runConfigValidate},
	}
	syncCommands = []commandSpec{
		{name: "plan", handler: runSyncPlan},
	}
	namespacesCommands = []commandSpec{
		{name: "ls", handler: runNamespacesList},
	}
	agentsCommands = []commandSpec{
		{name: "ls", handler: runAgentsList},
	}
	agentCommands = []commandSpec{
		{name: "show", handler: runAgentShow},
	}
	mailboxCommands = []commandSpec{
		{name: "ls", handler: runMailboxList},
		{name: "show", handler: runMailboxShow},
	}
	runsCommands = []commandSpec{
		{name: "ls", handler: runRunsList},
	}
	runCommands = []commandSpec{
		{name: "show", handler: runRunShow},
	}
	stepsCommands = []commandSpec{
		{name: "ls", handler: runStepsList},
	}
	schedulesCommands = []commandSpec{
		{name: "ls", handler: runSchedulesList},
	}
	threadCommands = []commandSpec{
		{name: "show", handler: runThreadShow},
	}
)

func runConfigCommand(ctx context.Context, args []string, streams commandIO) error {
	return dispatchCommands(ctx, "config", args, streams, configCommands)
}

func runSyncCommand(ctx context.Context, args []string, streams commandIO) error {
	return dispatchCommands(ctx, "sync", args, streams, syncCommands)
}

func runNamespacesCommand(ctx context.Context, args []string, streams commandIO) error {
	return dispatchCommands(ctx, "namespaces", args, streams, namespacesCommands)
}

func runAgentsCommand(ctx context.Context, args []string, streams commandIO) error {
	return dispatchCommands(ctx, "agents", args, streams, agentsCommands)
}

func runAgentCommand(ctx context.Context, args []string, streams commandIO) error {
	return dispatchCommands(ctx, "agent", args, streams, agentCommands)
}

func runMailboxCommand(ctx context.Context, args []string, streams commandIO) error {
	return dispatchCommands(ctx, "mailbox", args, streams, mailboxCommands)
}

func runRunsCommand(ctx context.Context, args []string, streams commandIO) error {
	return dispatchCommands(ctx, "runs", args, streams, runsCommands)
}

func runRunCommand(ctx context.Context, args []string, streams commandIO) error {
	return dispatchCommands(ctx, "run", args, streams, runCommands)
}

func runStepsCommand(ctx context.Context, args []string, streams commandIO) error {
	return dispatchCommands(ctx, "steps", args, streams, stepsCommands)
}

func runSchedulesCommand(ctx context.Context, args []string, streams commandIO) error {
	return dispatchCommands(ctx, "schedules", args, streams, schedulesCommands)
}

func runThreadCommand(ctx context.Context, args []string, streams commandIO) error {
	return dispatchCommands(ctx, "thread", args, streams, threadCommands)
}
