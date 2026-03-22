package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/richardartoul/swarmd/pkg/agent"
	"github.com/richardartoul/swarmd/pkg/sh/interp"
)

func TestOpenWorksWithRelativePath(t *testing.T) {
	ctx := context.Background()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd() error = %v", err)
	}
	tempDir := t.TempDir()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("os.Chdir(%q) error = %v", tempDir, err)
	}
	defer func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatalf("restore cwd error = %v", err)
		}
	}()

	s, err := Open(ctx, "./relative-server.db")
	if err != nil {
		t.Fatalf("Open() with relative path error = %v", err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	}()
}

func TestStoreClaimMessageAndCompleteRun(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)
	namespace := createTestNamespace(t, ctx, s, "namespace-one")
	agentRecord := createTestAgent(t, ctx, s, namespace.ID, "worker-a")

	message, err := s.EnqueueMessage(ctx, CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-1",
		RecipientAgentID: agentRecord.ID,
		Kind:             "user.prompt",
		Payload:          "  hello worker  ",
		Metadata:         map[string]any{"source": "test"},
		MaxAttempts:      3,
	})
	if err != nil {
		t.Fatalf("EnqueueMessage() error = %v", err)
	}

	const systemPrompt = "System prompt snapshot"
	claimed, err := s.ClaimNextMessage(ctx, ClaimMessageParams{
		NamespaceID:   namespace.ID,
		AgentID:       agentRecord.ID,
		LeaseDuration: time.Minute,
		SystemPrompt:  systemPrompt,
	})
	if err != nil {
		t.Fatalf("ClaimNextMessage() error = %v", err)
	}
	if claimed.Message.ID != message.ID {
		t.Fatalf("claimed message id = %q, want %q", claimed.Message.ID, message.ID)
	}
	if claimed.Message.AttemptCount != 1 {
		t.Fatalf("claimed attempt count = %d, want 1", claimed.Message.AttemptCount)
	}
	if claimed.Run.TriggerPrompt != "hello worker" {
		t.Fatalf("claimed run trigger prompt = %q, want %q", claimed.Run.TriggerPrompt, "hello worker")
	}
	if claimed.Run.SystemPrompt != systemPrompt {
		t.Fatalf("claimed run system prompt = %q, want %q", claimed.Run.SystemPrompt, systemPrompt)
	}

	if err := s.RecordStep(ctx, StepRecord{
		NamespaceID:       namespace.ID,
		RunID:             claimed.Run.ID,
		MessageID:         claimed.Message.ID,
		AgentID:           agentRecord.ID,
		StepIndex:         1,
		Thought:           "inspect prompt",
		Shell:             "printf 'hi'",
		UsageCachedTokens: 7,
		CWDBefore:         agentRecord.RootPath,
		CWDAfter:          agentRecord.RootPath,
		Stdout:            "hi",
		StartedAt:         time.Now().UTC(),
		FinishedAt:        time.Now().UTC().Add(10 * time.Millisecond),
		Duration:          10 * time.Millisecond,
		Status:            "ok",
	}); err != nil {
		t.Fatalf("RecordStep() error = %v", err)
	}

	if err := s.CompleteRun(ctx, CompleteRunParams{
		NamespaceID:       namespace.ID,
		RunID:             claimed.Run.ID,
		MessageID:         claimed.Message.ID,
		Status:            "finished",
		FinishedAt:        time.Now().UTC(),
		Duration:          25 * time.Millisecond,
		CWD:               agentRecord.RootPath,
		UsageCachedTokens: 11,
		FinishThought:     "the task is complete",
		Value:             map[string]any{"ok": true},
	}); err != nil {
		t.Fatalf("CompleteRun() error = %v", err)
	}

	runRecord, err := s.GetRun(ctx, namespace.ID, claimed.Run.ID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if runRecord.Status != "finished" {
		t.Fatalf("run status = %q, want %q", runRecord.Status, "finished")
	}
	if runRecord.ValueJSON == "" {
		t.Fatal("run value json was empty")
	}
	if runRecord.FinishThought != "the task is complete" {
		t.Fatalf("run finish thought = %q, want %q", runRecord.FinishThought, "the task is complete")
	}
	if runRecord.TriggerPrompt != "hello worker" {
		t.Fatalf("run trigger prompt = %q, want %q", runRecord.TriggerPrompt, "hello worker")
	}
	if runRecord.SystemPrompt != systemPrompt {
		t.Fatalf("run system prompt = %q, want %q", runRecord.SystemPrompt, systemPrompt)
	}

	var stepCount int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM steps WHERE namespace_id = ? AND run_id = ?`, namespace.ID, claimed.Run.ID).Scan(&stepCount); err != nil {
		t.Fatalf("count steps error = %v", err)
	}
	if stepCount != 1 {
		t.Fatalf("step count = %d, want 1", stepCount)
	}

	threadMessages, err := s.ListThreadMessages(ctx, namespace.ID, "thread-1", 10)
	if err != nil {
		t.Fatalf("ListThreadMessages() error = %v", err)
	}
	if len(threadMessages) != 1 {
		t.Fatalf("len(threadMessages) = %d, want 1", len(threadMessages))
	}
	if threadMessages[0].Status != MailboxMessageStatusCompleted {
		t.Fatalf("thread message status = %q, want %q", threadMessages[0].Status, MailboxMessageStatusCompleted)
	}

	snapshot, err := s.SnapshotNamespace(ctx, namespace.ID)
	if err != nil {
		t.Fatalf("SnapshotNamespace() error = %v", err)
	}
	if snapshot.Mailbox.Completed != 1 {
		t.Fatalf("completed mailbox count = %d, want 1", snapshot.Mailbox.Completed)
	}
}

func TestStoreScheduleLeaseRecoveryAndDeadLetter(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)
	namespace := createTestNamespace(t, ctx, s, "namespace-two")
	agentRecord := createTestAgent(t, ctx, s, namespace.ID, "worker-scheduled")

	schedule, err := s.CreateSchedule(ctx, CreateScheduleParams{
		NamespaceID: namespace.ID,
		AgentID:     agentRecord.ID,
		CronExpr:    "* * * * *",
		TimeZone:    "UTC",
		Payload:     "tick",
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("CreateSchedule() error = %v", err)
	}
	if schedule.NextFireAt == nil {
		t.Fatal("schedule next fire time was nil")
	}
	pastDue := time.Now().UTC().Add(-time.Second)
	if _, err := s.db.ExecContext(ctx, `UPDATE schedules SET next_fire_at_ms = ? WHERE namespace_id = ? AND schedule_id = ?`, toMillis(pastDue), namespace.ID, schedule.ID); err != nil {
		t.Fatalf("force schedule due error = %v", err)
	}

	enqueued, err := s.FireDueSchedules(ctx, time.Now().UTC(), 8)
	if err != nil {
		t.Fatalf("FireDueSchedules() error = %v", err)
	}
	if len(enqueued) != 1 {
		t.Fatalf("len(enqueued) = %d, want 1", len(enqueued))
	}

	claimed, err := s.ClaimNextMessage(ctx, ClaimMessageParams{
		NamespaceID:   namespace.ID,
		AgentID:       agentRecord.ID,
		LeaseDuration: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("ClaimNextMessage() error = %v", err)
	}

	time.Sleep(40 * time.Millisecond)

	reclaimed, err := s.ClaimNextMessage(ctx, ClaimMessageParams{
		NamespaceID:   namespace.ID,
		AgentID:       agentRecord.ID,
		LeaseDuration: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("ClaimNextMessage() after lease expiry error = %v", err)
	}
	if reclaimed.Message.ID != claimed.Message.ID {
		t.Fatalf("reclaimed message id = %q, want %q", reclaimed.Message.ID, claimed.Message.ID)
	}
	if reclaimed.Message.AttemptCount != 2 {
		t.Fatalf("reclaimed attempt count = %d, want 2", reclaimed.Message.AttemptCount)
	}

	retryAt := time.Now().UTC().Add(10 * time.Millisecond)
	if err := s.CompleteRun(ctx, CompleteRunParams{
		NamespaceID: namespace.ID,
		RunID:       reclaimed.Run.ID,
		MessageID:   reclaimed.Message.ID,
		Status:      "fatal_error",
		FinishedAt:  time.Now().UTC(),
		Duration:    5 * time.Millisecond,
		Error:       "boom",
		RetryAt:     &retryAt,
	}); err != nil {
		t.Fatalf("CompleteRun() retry error = %v", err)
	}

	time.Sleep(20 * time.Millisecond)

	thirdClaim, err := s.ClaimNextMessage(ctx, ClaimMessageParams{
		NamespaceID:   namespace.ID,
		AgentID:       agentRecord.ID,
		LeaseDuration: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("ClaimNextMessage() third attempt error = %v", err)
	}
	if thirdClaim.Message.AttemptCount != 3 {
		t.Fatalf("third claim attempt count = %d, want 3", thirdClaim.Message.AttemptCount)
	}

	if err := s.CompleteRun(ctx, CompleteRunParams{
		NamespaceID:      namespace.ID,
		RunID:            thirdClaim.Run.ID,
		MessageID:        thirdClaim.Message.ID,
		Status:           "fatal_error",
		FinishedAt:       time.Now().UTC(),
		Duration:         5 * time.Millisecond,
		Error:            "still broken",
		DeadLetterReason: "exhausted retries",
	}); err != nil {
		t.Fatalf("CompleteRun() dead letter error = %v", err)
	}

	snapshot, err := s.SnapshotNamespace(ctx, namespace.ID)
	if err != nil {
		t.Fatalf("SnapshotNamespace() error = %v", err)
	}
	if snapshot.Mailbox.DeadLetter != 1 {
		t.Fatalf("dead-letter mailbox count = %d, want 1", snapshot.Mailbox.DeadLetter)
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})
	return s
}

func createTestNamespace(t *testing.T, ctx context.Context, s *Store, name string) Namespace {
	t.Helper()
	namespace, err := s.CreateNamespace(ctx, CreateNamespaceParams{Name: name})
	if err != nil {
		t.Fatalf("CreateNamespace() error = %v", err)
	}
	return namespace
}

func createTestAgent(t *testing.T, ctx context.Context, s *Store, namespaceID, agentID string) RunnableAgent {
	t.Helper()
	root := filepath.Join(t.TempDir(), agentID)
	record, err := s.CreateAgent(ctx, CreateAgentParams{
		NamespaceID:  namespaceID,
		AgentID:      agentID,
		Name:         agentID,
		Role:         AgentRoleWorker,
		DesiredState: AgentDesiredStateRunning,
		RootPath:     root,
		ModelName:    "test-model",
		SystemPrompt: "You are a test worker.",
		MaxAttempts:  3,
	})
	if err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}
	return record
}

func TestCreateAgentDefaultsStepTimeoutToFiveMinutes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)
	namespace := createTestNamespace(t, ctx, s, "namespace-step-timeout")
	root := filepath.Join(t.TempDir(), "worker")

	record, err := s.CreateAgent(ctx, CreateAgentParams{
		NamespaceID:  namespace.ID,
		AgentID:      "worker",
		Name:         "worker",
		Role:         AgentRoleWorker,
		DesiredState: AgentDesiredStateRunning,
		RootPath:     root,
		ModelName:    "test-model",
		SystemPrompt: "You are a test worker.",
	})
	if err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}
	if record.StepTimeout != DefaultAgentStepTimeout {
		t.Fatalf("record.StepTimeout = %v, want %v", record.StepTimeout, DefaultAgentStepTimeout)
	}

	loaded, err := s.GetAgent(ctx, namespace.ID, "worker")
	if err != nil {
		t.Fatalf("GetAgent() error = %v", err)
	}
	if loaded.StepTimeout != DefaultAgentStepTimeout {
		t.Fatalf("loaded.StepTimeout = %v, want %v", loaded.StepTimeout, DefaultAgentStepTimeout)
	}
}

func TestStorePersistsAgentConfigTools(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)
	namespace := createTestNamespace(t, ctx, s, "namespace-tools")

	root := filepath.Join(t.TempDir(), "worker")
	created, err := s.CreateAgent(ctx, CreateAgentParams{
		NamespaceID:  namespace.ID,
		AgentID:      "worker",
		Name:         "worker",
		Role:         AgentRoleWorker,
		DesiredState: AgentDesiredStateRunning,
		RootPath:     root,
		ModelName:    "test-model",
		SystemPrompt: "You are a test worker.",
		Config: map[string]any{
			"tools": []agent.ConfiguredTool{{
				ID: "example_tool",
				Config: map[string]any{
					"default_channel": "C12345678",
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}
	if !strings.Contains(created.ConfigJSON, `"tools"`) || !strings.Contains(created.ConfigJSON, `"example_tool"`) {
		t.Fatalf("created.ConfigJSON = %q, want persisted tools payload", created.ConfigJSON)
	}

	loaded, err := s.GetAgent(ctx, namespace.ID, "worker")
	if err != nil {
		t.Fatalf("GetAgent() error = %v", err)
	}
	if !strings.Contains(loaded.ConfigJSON, `"tools"`) || !strings.Contains(loaded.ConfigJSON, `"example_tool"`) {
		t.Fatalf("loaded.ConfigJSON = %q, want persisted tools payload", loaded.ConfigJSON)
	}
}

func TestPutAgentUpdatesPromptWhenActionSchemaChanges(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)
	namespace := createTestNamespace(t, ctx, s, "namespace-action-schema")
	root := filepath.Join(t.TempDir(), "worker")

	firstSchema, err := agent.ResolveActionSchema(nil, nil)
	if err != nil {
		t.Fatalf("ResolveActionSchema() first error = %v", err)
	}
	created, err := s.CreateAgent(ctx, CreateAgentParams{
		NamespaceID:  namespace.ID,
		AgentID:      "worker",
		Name:         "worker",
		Role:         AgentRoleWorker,
		DesiredState: AgentDesiredStateRunning,
		RootPath:     root,
		ModelName:    "test-model",
		SystemPrompt: "You are a test worker.",
		ActionSchema: firstSchema,
	})
	if err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}

	secondSchema, err := agent.ResolveActionSchema(nil, []interp.HostMatcher{{Glob: "*"}})
	if err != nil {
		t.Fatalf("ResolveActionSchema() second error = %v", err)
	}
	result, err := s.PutAgent(ctx, CreateAgentParams{
		NamespaceID:  namespace.ID,
		AgentID:      "worker",
		Name:         "worker",
		Role:         AgentRoleWorker,
		DesiredState: AgentDesiredStateRunning,
		RootPath:     root,
		ModelName:    "test-model",
		SystemPrompt: "You are a test worker.",
		ActionSchema: secondSchema,
	})
	if err != nil {
		t.Fatalf("PutAgent() error = %v", err)
	}
	if !result.Updated {
		t.Fatal("PutAgent() Updated = false, want true when action schema changes")
	}
	if result.Agent.CurrentPromptVersionID == created.CurrentPromptVersionID {
		t.Fatal("CurrentPromptVersionID did not change after action schema update")
	}

	var actionSchemaJSON string
	if err := s.db.QueryRowContext(
		ctx,
		`SELECT action_schema_json FROM agent_prompt_versions WHERE namespace_id = ? AND prompt_version_id = ?`,
		namespace.ID,
		result.Agent.CurrentPromptVersionID,
	).Scan(&actionSchemaJSON); err != nil {
		t.Fatalf("load updated action schema json error = %v", err)
	}
	var stored map[string]any
	if err := DecodeEnvelopeInto(actionSchemaJSON, &stored); err != nil {
		t.Fatalf("DecodeEnvelopeInto() error = %v", err)
	}
	tools, ok := stored["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("stored action schema = %#v, want non-empty tool list", stored)
	}
	foundWebSearch := false
	for _, rawTool := range tools {
		toolMap, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		if toolMap["name"] == agent.ToolNameWebSearch {
			foundWebSearch = true
			break
		}
	}
	if !foundWebSearch {
		t.Fatalf("stored action schema = %#v, want network-enabled tool %q after schema update", stored, agent.ToolNameWebSearch)
	}
}
