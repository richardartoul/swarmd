package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

func openReadOnlyStore(ctx context.Context, fs *flag.FlagSet, dbPath string) (*cpstore.Store, string, error) {
	resolvedDBPath, err := resolveReadOnlyDBPath(fs, dbPath)
	if err != nil {
		return nil, "", err
	}
	store, err := cpstore.OpenReadOnly(ctx, resolvedDBPath)
	if err != nil {
		return nil, "", err
	}
	return store, resolvedDBPath, nil
}

func resolveReadOnlyDBPath(fs *flag.FlagSet, dbPath string) (string, error) {
	dbPath = strings.TrimSpace(dbPath)
	if dbPath == "" {
		return "", fmt.Errorf("missing sqlite database path")
	}
	if flagWasSet(fs, "db") || strings.TrimSpace(os.Getenv("SWARMD_SERVER_DB")) != "" {
		return dbPath, nil
	}
	absPath, ok, err := sqliteLocalPathAbs(dbPath)
	if err != nil {
		return "", err
	}
	if !ok {
		return dbPath, nil
	}
	if _, err := os.Stat(absPath); err == nil {
		return dbPath, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat sqlite database %q: %w", absPath, err)
	}
	fallbackPath, err := findLocalSQLiteFallback(absPath)
	if err != nil {
		return "", err
	}
	if fallbackPath != "" {
		return fallbackPath, nil
	}
	return dbPath, nil
}

func findLocalSQLiteFallback(missingPath string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("default sqlite database %q does not exist and current directory lookup failed: %w", missingPath, err)
	}
	entries, err := os.ReadDir(cwd)
	if err != nil {
		return "", fmt.Errorf("default sqlite database %q does not exist and local .db search in %q failed: %w", missingPath, cwd, err)
	}
	candidates := make([]string, 0)
	preferred := ""
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return "", fmt.Errorf("inspect local sqlite candidate %q: %w", filepath.Join(cwd, entry.Name()), err)
		}
		if info.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".db") {
			continue
		}
		candidate := filepath.Join(cwd, entry.Name())
		candidates = append(candidates, candidate)
		if entry.Name() == defaultSQLiteFilename {
			preferred = candidate
		}
	}
	sort.Strings(candidates)
	if preferred != "" {
		return preferred, nil
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	if len(candidates) == 0 {
		return "", nil
	}
	names := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		names = append(names, filepath.Base(candidate))
	}
	return "", fmt.Errorf(
		"default sqlite database %q does not exist and multiple local .db files were found in %q (%s); pass -db or set SWARMD_SERVER_DB",
		missingPath,
		cwd,
		strings.Join(names, ", "),
	)
}

func ensureNoExtraArgs(fs *flag.FlagSet, usage string) error {
	if fs.NArg() == 0 {
		return nil
	}
	return fmt.Errorf("%s does not accept positional arguments", usage)
}

func requireFlag(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("missing required -%s", name)
	}
	return nil
}

func parseMailboxStatus(raw string) (cpstore.MailboxMessageStatus, error) {
	switch strings.TrimSpace(raw) {
	case "":
		return "", nil
	case string(cpstore.MailboxMessageStatusQueued):
		return cpstore.MailboxMessageStatusQueued, nil
	case string(cpstore.MailboxMessageStatusLeased):
		return cpstore.MailboxMessageStatusLeased, nil
	case string(cpstore.MailboxMessageStatusCompleted):
		return cpstore.MailboxMessageStatusCompleted, nil
	case string(cpstore.MailboxMessageStatusDeadLetter):
		return cpstore.MailboxMessageStatusDeadLetter, nil
	default:
		return "", fmt.Errorf("invalid mailbox status %q", raw)
	}
}

func loadSchedules(ctx context.Context, store *cpstore.Store, namespaceID, agentID string) ([]cpstore.ScheduleRecord, error) {
	if strings.TrimSpace(namespaceID) != "" {
		schedules, err := store.ListSchedules(ctx, namespaceID)
		if err != nil {
			return nil, err
		}
		filtered := filterSchedulesByAgent(schedules, agentID)
		sortSchedules(filtered)
		return filtered, nil
	}

	namespaces, err := store.ListNamespaces(ctx)
	if err != nil {
		return nil, err
	}
	var schedules []cpstore.ScheduleRecord
	for _, ns := range namespaces {
		nsSchedules, err := store.ListSchedules(ctx, ns.ID)
		if err != nil {
			return nil, err
		}
		schedules = append(schedules, filterSchedulesByAgent(nsSchedules, agentID)...)
	}
	sortSchedules(schedules)
	return schedules, nil
}

func filterSchedulesByAgent(schedules []cpstore.ScheduleRecord, agentID string) []cpstore.ScheduleRecord {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		cloned := append([]cpstore.ScheduleRecord(nil), schedules...)
		sortSchedules(cloned)
		return cloned
	}
	filtered := make([]cpstore.ScheduleRecord, 0, len(schedules))
	for _, schedule := range schedules {
		if schedule.AgentID == agentID {
			filtered = append(filtered, schedule)
		}
	}
	sortSchedules(filtered)
	return filtered
}

func sortSchedules(schedules []cpstore.ScheduleRecord) {
	sort.Slice(schedules, func(i, j int) bool {
		if schedules[i].NamespaceID != schedules[j].NamespaceID {
			return schedules[i].NamespaceID < schedules[j].NamespaceID
		}
		if schedules[i].AgentID != schedules[j].AgentID {
			return schedules[i].AgentID < schedules[j].AgentID
		}
		return schedules[i].ID < schedules[j].ID
	})
}
