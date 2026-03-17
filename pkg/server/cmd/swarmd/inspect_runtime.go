package main

import (
	"context"
	"errors"
	"flag"
	"strings"

	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

func runMailboxList(ctx context.Context, args []string, streams commandIO) error {
	fs := flag.NewFlagSet("mailbox ls", flag.ContinueOnError)
	fs.SetOutput(streams.stderr)

	dbPath := envOr("SWARMD_SERVER_DB", defaultSQLitePath)
	namespaceID := ""
	agentID := ""
	status := ""
	limit := 50
	fs.StringVar(&dbPath, "db", dbPath, "SQLite database path")
	fs.StringVar(&namespaceID, "namespace", namespaceID, "Filter by namespace id")
	fs.StringVar(&agentID, "agent", agentID, "Filter by sender or recipient agent id")
	fs.StringVar(&status, "status", status, "Filter by mailbox status")
	fs.IntVar(&limit, "limit", limit, "Maximum number of messages to show")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := ensureNoExtraArgs(fs, "mailbox ls"); err != nil {
		return err
	}

	statusValue, err := parseMailboxStatus(status)
	if err != nil {
		return err
	}

	store, _, err := openReadOnlyStore(ctx, fs, dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	messages, err := store.ListMailboxMessages(ctx, cpstore.ListMailboxMessagesParams{
		NamespaceID: strings.TrimSpace(namespaceID),
		AgentID:     strings.TrimSpace(agentID),
		Status:      statusValue,
		Limit:       limit,
	})
	if err != nil {
		return err
	}
	renderMailboxList(streams.stdout, messages)
	return nil
}

func runMailboxShow(ctx context.Context, args []string, streams commandIO) error {
	fs := flag.NewFlagSet("mailbox show", flag.ContinueOnError)
	fs.SetOutput(streams.stderr)

	dbPath := envOr("SWARMD_SERVER_DB", defaultSQLitePath)
	namespaceID := ""
	messageID := ""
	fs.StringVar(&dbPath, "db", dbPath, "SQLite database path")
	fs.StringVar(&namespaceID, "namespace", namespaceID, "Namespace id")
	fs.StringVar(&messageID, "message", messageID, "Mailbox message id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := ensureNoExtraArgs(fs, "mailbox show"); err != nil {
		return err
	}
	if err := requireFlag("namespace", namespaceID); err != nil {
		return err
	}
	if err := requireFlag("message", messageID); err != nil {
		return err
	}

	store, _, err := openReadOnlyStore(ctx, fs, dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	message, err := store.GetMailboxMessage(ctx, namespaceID, messageID)
	if err != nil {
		return err
	}
	renderMailboxShow(streams.stdout, message)
	return nil
}

func runRunsList(ctx context.Context, args []string, streams commandIO) error {
	fs := flag.NewFlagSet("runs ls", flag.ContinueOnError)
	fs.SetOutput(streams.stderr)

	dbPath := envOr("SWARMD_SERVER_DB", defaultSQLitePath)
	namespaceID := ""
	agentID := ""
	status := ""
	limit := 50
	fs.StringVar(&dbPath, "db", dbPath, "SQLite database path")
	fs.StringVar(&namespaceID, "namespace", namespaceID, "Filter by namespace id")
	fs.StringVar(&agentID, "agent", agentID, "Filter by agent id")
	fs.StringVar(&status, "status", status, "Filter by run status")
	fs.IntVar(&limit, "limit", limit, "Maximum number of runs to show")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := ensureNoExtraArgs(fs, "runs ls"); err != nil {
		return err
	}

	store, _, err := openReadOnlyStore(ctx, fs, dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	runs, err := store.ListRuns(ctx, cpstore.ListRunsParams{
		NamespaceID: strings.TrimSpace(namespaceID),
		AgentID:     strings.TrimSpace(agentID),
		Status:      strings.TrimSpace(status),
		Limit:       limit,
	})
	if err != nil {
		return err
	}
	renderRunList(streams.stdout, runs)
	return nil
}

func runRunShow(ctx context.Context, args []string, streams commandIO) error {
	fs := flag.NewFlagSet("run show", flag.ContinueOnError)
	fs.SetOutput(streams.stderr)

	dbPath := envOr("SWARMD_SERVER_DB", defaultSQLitePath)
	namespaceID := ""
	runID := ""
	fs.StringVar(&dbPath, "db", dbPath, "SQLite database path")
	fs.StringVar(&namespaceID, "namespace", namespaceID, "Namespace id")
	fs.StringVar(&runID, "run", runID, "Run id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := ensureNoExtraArgs(fs, "run show"); err != nil {
		return err
	}
	if err := requireFlag("namespace", namespaceID); err != nil {
		return err
	}
	if err := requireFlag("run", runID); err != nil {
		return err
	}

	store, _, err := openReadOnlyStore(ctx, fs, dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	runRecord, err := store.GetRun(ctx, namespaceID, runID)
	if err != nil {
		return err
	}
	steps, err := store.ListStepsByRun(ctx, namespaceID, runID)
	if err != nil {
		return err
	}
	var message *cpstore.MailboxMessageRecord
	if runRecord.MessageID != "" {
		record, err := store.GetMailboxMessage(ctx, namespaceID, runRecord.MessageID)
		if err != nil && !errors.Is(err, cpstore.ErrNotFound) {
			return err
		}
		if err == nil {
			message = &record
		}
	}
	renderRunShow(streams.stdout, runShowData{
		Run:     runRecord,
		Message: message,
		Steps:   steps,
	})
	return nil
}

func runStepsList(ctx context.Context, args []string, streams commandIO) error {
	fs := flag.NewFlagSet("steps ls", flag.ContinueOnError)
	fs.SetOutput(streams.stderr)

	dbPath := envOr("SWARMD_SERVER_DB", defaultSQLitePath)
	namespaceID := ""
	runID := ""
	fs.StringVar(&dbPath, "db", dbPath, "SQLite database path")
	fs.StringVar(&namespaceID, "namespace", namespaceID, "Namespace id")
	fs.StringVar(&runID, "run", runID, "Run id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := ensureNoExtraArgs(fs, "steps ls"); err != nil {
		return err
	}
	if err := requireFlag("namespace", namespaceID); err != nil {
		return err
	}
	if err := requireFlag("run", runID); err != nil {
		return err
	}

	store, _, err := openReadOnlyStore(ctx, fs, dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	steps, err := store.ListStepsByRun(ctx, namespaceID, runID)
	if err != nil {
		return err
	}
	renderStepList(streams.stdout, steps)
	return nil
}

func runThreadShow(ctx context.Context, args []string, streams commandIO) error {
	fs := flag.NewFlagSet("thread show", flag.ContinueOnError)
	fs.SetOutput(streams.stderr)

	dbPath := envOr("SWARMD_SERVER_DB", defaultSQLitePath)
	namespaceID := ""
	threadID := ""
	limit := 50
	fs.StringVar(&dbPath, "db", dbPath, "SQLite database path")
	fs.StringVar(&namespaceID, "namespace", namespaceID, "Namespace id")
	fs.StringVar(&threadID, "thread", threadID, "Thread id")
	fs.IntVar(&limit, "limit", limit, "Maximum number of thread messages to show")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := ensureNoExtraArgs(fs, "thread show"); err != nil {
		return err
	}
	if err := requireFlag("namespace", namespaceID); err != nil {
		return err
	}
	if err := requireFlag("thread", threadID); err != nil {
		return err
	}

	store, _, err := openReadOnlyStore(ctx, fs, dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	messages, err := store.ListThreadMessages(ctx, namespaceID, threadID, limit)
	if err != nil {
		return err
	}
	renderThreadShow(streams.stdout, threadShowData{
		NamespaceID: namespaceID,
		ThreadID:    threadID,
		Messages:    messages,
	})
	return nil
}
