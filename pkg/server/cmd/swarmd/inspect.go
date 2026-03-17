package main

import (
	"context"
	"flag"
	"os"
	"strings"

	"github.com/richardartoul/swarmd/pkg/server"
	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

func runConfigValidate(_ context.Context, args []string, streams commandIO) error {
	fs := flag.NewFlagSet("config validate", flag.ContinueOnError)
	fs.SetOutput(streams.stderr)

	configRoot := envOr("SWARMD_SERVER_CONFIG_ROOT", defaultConfigRoot)
	fs.StringVar(&configRoot, "config-root", configRoot, "Config root containing namespaces/<namespace>/agents/*.yaml")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := ensureNoExtraArgs(fs, "config validate"); err != nil {
		return err
	}

	specs, err := server.LoadAgentSpecs(configRoot)
	if err != nil {
		return err
	}
	if err := server.ValidateReferencedToolEnv(specs, os.Getenv); err != nil {
		return err
	}
	if err := server.ValidateReferencedAgentConfigEnv(specs, os.Getenv); err != nil {
		return err
	}
	renderConfigValidation(streams.stdout, configRoot, server.SummarizeAgentSpecs(specs))
	return nil
}

func runSyncPlan(ctx context.Context, args []string, streams commandIO) error {
	fs := flag.NewFlagSet("sync plan", flag.ContinueOnError)
	fs.SetOutput(streams.stderr)

	dbPath := envOr("SWARMD_SERVER_DB", defaultSQLitePath)
	configRoot := envOr("SWARMD_SERVER_CONFIG_ROOT", defaultConfigRoot)
	rootBase := envOr("SWARMD_SERVER_ROOT_BASE", defaultRootBase)
	fs.StringVar(&dbPath, "db", dbPath, "SQLite database path")
	fs.StringVar(&configRoot, "config-root", configRoot, "Config root containing namespaces/<namespace>/agents/*.yaml")
	fs.StringVar(&rootBase, "root-base", rootBase, "Default base directory for agent work roots")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := ensureNoExtraArgs(fs, "sync plan"); err != nil {
		return err
	}

	store, resolvedDBPath, err := openReadOnlyStore(ctx, fs, dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	plan, err := server.PlanSyncFromConfigRoot(ctx, store, configRoot, rootBase)
	if err != nil {
		return err
	}
	renderSyncPlanOutput(streams.stdout, resolvedDBPath, configRoot, plan)
	return nil
}

func runNamespacesList(ctx context.Context, args []string, streams commandIO) error {
	fs := flag.NewFlagSet("namespaces ls", flag.ContinueOnError)
	fs.SetOutput(streams.stderr)

	dbPath := envOr("SWARMD_SERVER_DB", defaultSQLitePath)
	fs.StringVar(&dbPath, "db", dbPath, "SQLite database path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := ensureNoExtraArgs(fs, "namespaces ls"); err != nil {
		return err
	}

	store, _, err := openReadOnlyStore(ctx, fs, dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	namespaces, err := store.ListNamespaces(ctx)
	if err != nil {
		return err
	}
	rows := make([]namespaceInspectRow, 0, len(namespaces))
	for _, ns := range namespaces {
		snapshot, err := store.SnapshotNamespace(ctx, ns.ID)
		if err != nil {
			return err
		}
		rows = append(rows, namespaceInspectRow{
			Namespace:     snapshot.Namespace,
			AgentCount:    len(snapshot.Agents),
			ScheduleCount: len(snapshot.Schedules),
			Mailbox:       snapshot.Mailbox,
		})
	}
	renderNamespaceList(streams.stdout, rows)
	return nil
}

func runAgentsList(ctx context.Context, args []string, streams commandIO) error {
	fs := flag.NewFlagSet("agents ls", flag.ContinueOnError)
	fs.SetOutput(streams.stderr)

	dbPath := envOr("SWARMD_SERVER_DB", defaultSQLitePath)
	namespaceID := ""
	fs.StringVar(&dbPath, "db", dbPath, "SQLite database path")
	fs.StringVar(&namespaceID, "namespace", namespaceID, "Filter by namespace id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := ensureNoExtraArgs(fs, "agents ls"); err != nil {
		return err
	}

	store, _, err := openReadOnlyStore(ctx, fs, dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	agents, err := store.ListAgents(ctx, cpstore.ListAgentsParams{NamespaceID: strings.TrimSpace(namespaceID)})
	if err != nil {
		return err
	}
	renderAgentList(streams.stdout, agents)
	return nil
}

func runAgentShow(ctx context.Context, args []string, streams commandIO) error {
	fs := flag.NewFlagSet("agent show", flag.ContinueOnError)
	fs.SetOutput(streams.stderr)

	dbPath := envOr("SWARMD_SERVER_DB", defaultSQLitePath)
	namespaceID := ""
	agentID := ""
	fs.StringVar(&dbPath, "db", dbPath, "SQLite database path")
	fs.StringVar(&namespaceID, "namespace", namespaceID, "Namespace id")
	fs.StringVar(&agentID, "agent", agentID, "Agent id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := ensureNoExtraArgs(fs, "agent show"); err != nil {
		return err
	}
	if err := requireFlag("namespace", namespaceID); err != nil {
		return err
	}
	if err := requireFlag("agent", agentID); err != nil {
		return err
	}

	store, _, err := openReadOnlyStore(ctx, fs, dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	agent, err := store.GetAgent(ctx, namespaceID, agentID)
	if err != nil {
		return err
	}
	schedules, err := store.ListSchedules(ctx, namespaceID)
	if err != nil {
		return err
	}
	filtered := filterSchedulesByAgent(schedules, agentID)
	renderAgentShow(streams.stdout, agentShowData{
		Agent:     agent,
		Schedules: filtered,
	})
	return nil
}

func runSchedulesList(ctx context.Context, args []string, streams commandIO) error {
	fs := flag.NewFlagSet("schedules ls", flag.ContinueOnError)
	fs.SetOutput(streams.stderr)

	dbPath := envOr("SWARMD_SERVER_DB", defaultSQLitePath)
	namespaceID := ""
	agentID := ""
	fs.StringVar(&dbPath, "db", dbPath, "SQLite database path")
	fs.StringVar(&namespaceID, "namespace", namespaceID, "Filter by namespace id")
	fs.StringVar(&agentID, "agent", agentID, "Filter by agent id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := ensureNoExtraArgs(fs, "schedules ls"); err != nil {
		return err
	}

	store, _, err := openReadOnlyStore(ctx, fs, dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	schedules, err := loadSchedules(ctx, store, strings.TrimSpace(namespaceID), strings.TrimSpace(agentID))
	if err != nil {
		return err
	}
	renderScheduleList(streams.stdout, schedules)
	return nil
}
