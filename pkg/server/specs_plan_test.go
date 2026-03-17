package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

func TestPlanSyncFromConfigRootDetectsCreatesUpdatesAndDeletes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	tempDir := t.TempDir()
	configRoot := filepath.Join(tempDir, "config")
	rootBase := filepath.Join(tempDir, "roots")
	if err := os.MkdirAll(filepath.Join(configRoot, "namespaces", "default", "agents"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	writeSpecFile(t, filepath.Join(configRoot, "namespaces", "default", "agents", "worker-a.yaml"), `
version: 1
model:
  name: new-model
prompt: |
  Updated prompt
network:
  reachable_hosts:
    - glob: "*"
tools:
  - server_log
runtime:
  max_steps: 7
schedules:
  - id: sched-a
    cron: "* * * * *"
    timezone: UTC
    payload:
      kind: ping
`)
	writeSpecFile(t, filepath.Join(configRoot, "namespaces", "default", "agents", "worker-b.yaml"), `
version: 1
model:
  name: model-b
prompt: |
  New worker prompt
schedules:
  - id: sched-b
    cron: "*/5 * * * *"
    timezone: UTC
`)

	store := openPlanTestStore(t, filepath.Join(tempDir, "server.db"))
	defer store.Close()

	if _, err := store.PutNamespace(ctx, cpstore.CreateNamespaceParams{ID: "default", Name: "default"}); err != nil {
		t.Fatalf("PutNamespace(default) error = %v", err)
	}
	if _, err := store.PutAgent(ctx, cpstore.CreateAgentParams{
		NamespaceID:    "default",
		AgentID:        "worker-a",
		Name:           "worker-a",
		RootPath:       filepath.Join(rootBase, "default", "worker-a"),
		ModelName:      "old-model",
		SystemPrompt:   "Old prompt",
		MaxSteps:       2,
		AllowNetwork:   false,
		DesiredState:   cpstore.AgentDesiredStateRunning,
		Role:           cpstore.AgentRoleWorker,
		Config:         map[string]any{"managed_by": "filesystem", "source_path": "old-path"},
		MaxAttempts:    2,
		LeaseDuration:  10,
		RetryDelay:     5,
		StepTimeout:    3,
		MaxOutputBytes: 128,
	}); err != nil {
		t.Fatalf("PutAgent(worker-a) error = %v", err)
	}
	if _, err := store.CreateSchedule(ctx, cpstore.CreateScheduleParams{
		NamespaceID: "default",
		ScheduleID:  "sched-a",
		AgentID:     "worker-a",
		CronExpr:    "*/10 * * * *",
		TimeZone:    "UTC",
		Payload:     map[string]any{"kind": "old"},
		Enabled:     true,
	}); err != nil {
		t.Fatalf("CreateSchedule(sched-a) error = %v", err)
	}
	if _, err := store.PutAgent(ctx, cpstore.CreateAgentParams{
		NamespaceID:  "default",
		AgentID:      "stale-agent",
		Name:         "stale-agent",
		RootPath:     filepath.Join(rootBase, "default", "stale-agent"),
		ModelName:    "stale-model",
		SystemPrompt: "Stale prompt",
	}); err != nil {
		t.Fatalf("PutAgent(stale-agent) error = %v", err)
	}
	if _, err := store.CreateSchedule(ctx, cpstore.CreateScheduleParams{
		NamespaceID: "default",
		ScheduleID:  "stale-sched",
		AgentID:     "stale-agent",
		CronExpr:    "0 * * * *",
		TimeZone:    "UTC",
		Payload:     "stale",
		Enabled:     true,
	}); err != nil {
		t.Fatalf("CreateSchedule(stale-sched) error = %v", err)
	}

	plan, err := PlanSyncFromConfigRoot(ctx, store, configRoot, rootBase)
	if err != nil {
		t.Fatalf("PlanSyncFromConfigRoot() error = %v", err)
	}

	if plan.Summary.AgentsCreated != 1 || plan.Summary.AgentsUpdated != 1 || plan.Summary.AgentsDeleted != 1 {
		t.Fatalf("agent summary = %#v, want create=1 update=1 delete=1", plan.Summary)
	}
	if plan.Summary.SchedulesCreated != 1 || plan.Summary.SchedulesUpdated != 1 || plan.Summary.SchedulesDeleted != 1 {
		t.Fatalf("schedule summary = %#v, want create=1 update=1 delete=1", plan.Summary)
	}

	assertAgentChange(t, plan, ChangeActionCreate, "default", "worker-b")
	update := assertAgentChange(t, plan, ChangeActionUpdate, "default", "worker-a")
	assertFieldChanged(t, update.Changes, "model_name")
	assertFieldChanged(t, update.Changes, "system_prompt")
	assertFieldChanged(t, update.Changes, "config")
	assertAgentChange(t, plan, ChangeActionDelete, "default", "stale-agent")

	assertScheduleChange(t, plan, ChangeActionCreate, "default", "worker-b", "sched-b")
	scheduleUpdate := assertScheduleChange(t, plan, ChangeActionUpdate, "default", "worker-a", "sched-a")
	assertFieldChanged(t, scheduleUpdate.Changes, "cron_expr")
	assertFieldChanged(t, scheduleUpdate.Changes, "payload")
	assertScheduleChange(t, plan, ChangeActionDelete, "default", "stale-agent", "stale-sched")
}

func openPlanTestStore(t *testing.T, dbPath string) *cpstore.Store {
	t.Helper()
	store, err := cpstore.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("cpstore.Open() error = %v", err)
	}
	return store
}

func writeSpecFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}

func assertAgentChange(t *testing.T, plan SyncPlan, action ChangeAction, namespaceID, agentID string) AgentPlanChange {
	t.Helper()
	for _, change := range plan.AgentChanges {
		if change.Action == action && change.NamespaceID == namespaceID && change.AgentID == agentID {
			return change
		}
	}
	t.Fatalf("missing agent change %s %s/%s in %#v", action, namespaceID, agentID, plan.AgentChanges)
	return AgentPlanChange{}
}

func assertScheduleChange(t *testing.T, plan SyncPlan, action ChangeAction, namespaceID, agentID, scheduleID string) SchedulePlanChange {
	t.Helper()
	for _, change := range plan.ScheduleChanges {
		if change.Action == action && change.NamespaceID == namespaceID && change.AgentID == agentID && change.ScheduleID == scheduleID {
			return change
		}
	}
	t.Fatalf("missing schedule change %s %s/%s/%s in %#v", action, namespaceID, agentID, scheduleID, plan.ScheduleChanges)
	return SchedulePlanChange{}
}

func assertFieldChanged(t *testing.T, changes []FieldChange, field string) {
	t.Helper()
	for _, change := range changes {
		if change.Field == field {
			return
		}
	}
	t.Fatalf("field %q not found in changes %#v", field, changes)
}
