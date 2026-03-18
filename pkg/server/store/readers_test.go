package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreListReaders(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)
	namespace := createTestNamespace(t, ctx, s, "reader-namespace")
	agentRecord := createTestAgent(t, ctx, s, namespace.ID, "reader-agent")

	message, err := s.EnqueueMessage(ctx, CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-readers",
		RecipientAgentID: agentRecord.ID,
		Kind:             "user.prompt",
		Payload:          map[string]any{"text": "hello"},
		Metadata:         map[string]any{"source": "test"},
		MaxAttempts:      2,
	})
	if err != nil {
		t.Fatalf("EnqueueMessage() error = %v", err)
	}

	claimed, err := s.ClaimNextMessage(ctx, ClaimMessageParams{
		NamespaceID:   namespace.ID,
		AgentID:       agentRecord.ID,
		LeaseDuration: time.Minute,
		SystemPrompt:  "Reader system prompt",
	})
	if err != nil {
		t.Fatalf("ClaimNextMessage() error = %v", err)
	}

	startedAt := time.Now().UTC()
	finishedAt := startedAt.Add(15 * time.Millisecond)
	if err := s.RecordStep(ctx, StepRecord{
		NamespaceID:           namespace.ID,
		RunID:                 claimed.Run.ID,
		MessageID:             claimed.Message.ID,
		AgentID:               agentRecord.ID,
		StepIndex:             1,
		StepType:              "tool",
		Thought:               "read config",
		ActionName:            "read_file",
		ActionToolKind:        "function",
		ActionInput:           `{"file_path":"config.yaml"}`,
		ActionOutput:          "1|name: demo\n2|enabled: true\n",
		ActionOutputTruncated: true,
		UsageCachedTokens:     3,
		CWDBefore:             agentRecord.RootPath,
		CWDAfter:              agentRecord.RootPath,
		StartedAt:             startedAt,
		FinishedAt:            finishedAt,
		Duration:              finishedAt.Sub(startedAt),
		Status:                "ok",
	}); err != nil {
		t.Fatalf("RecordStep() error = %v", err)
	}

	if err := s.CompleteRun(ctx, CompleteRunParams{
		NamespaceID:       namespace.ID,
		RunID:             claimed.Run.ID,
		MessageID:         claimed.Message.ID,
		Status:            "finished",
		FinishedAt:        finishedAt,
		Duration:          finishedAt.Sub(startedAt),
		CWD:               agentRecord.RootPath,
		UsageCachedTokens: 4,
		FinishThought:     "finished after reading the config",
		Value:             map[string]any{"ok": true},
	}); err != nil {
		t.Fatalf("CompleteRun() error = %v", err)
	}

	agents, err := s.ListAgents(ctx, ListAgentsParams{NamespaceID: namespace.ID})
	if err != nil {
		t.Fatalf("ListAgents() error = %v", err)
	}
	if len(agents) != 1 || agents[0].ID != agentRecord.ID {
		t.Fatalf("ListAgents() = %#v, want one agent %q", agents, agentRecord.ID)
	}

	messages, err := s.ListMailboxMessages(ctx, ListMailboxMessagesParams{
		NamespaceID: namespace.ID,
		AgentID:     agentRecord.ID,
		Status:      MailboxMessageStatusCompleted,
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("ListMailboxMessages() error = %v", err)
	}
	if len(messages) != 1 || messages[0].ID != message.ID {
		t.Fatalf("ListMailboxMessages() = %#v, want one message %q", messages, message.ID)
	}

	messageRecord, err := s.GetMailboxMessage(ctx, namespace.ID, message.ID)
	if err != nil {
		t.Fatalf("GetMailboxMessage() error = %v", err)
	}
	if messageRecord.Status != MailboxMessageStatusCompleted {
		t.Fatalf("GetMailboxMessage().Status = %q, want %q", messageRecord.Status, MailboxMessageStatusCompleted)
	}

	runs, err := s.ListRuns(ctx, ListRunsParams{
		NamespaceID: namespace.ID,
		AgentID:     agentRecord.ID,
		Status:      "finished",
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	if len(runs) != 1 || runs[0].ID != claimed.Run.ID {
		t.Fatalf("ListRuns() = %#v, want one run %q", runs, claimed.Run.ID)
	}
	if runs[0].TriggerPrompt != "{\n  \"text\": \"hello\"\n}" {
		t.Fatalf("ListRuns()[0].TriggerPrompt = %q, want rendered payload JSON", runs[0].TriggerPrompt)
	}
	if runs[0].SystemPrompt != "Reader system prompt" {
		t.Fatalf("ListRuns()[0].SystemPrompt = %q, want %q", runs[0].SystemPrompt, "Reader system prompt")
	}
	if runs[0].FinishThought != "finished after reading the config" {
		t.Fatalf("ListRuns()[0].FinishThought = %q, want %q", runs[0].FinishThought, "finished after reading the config")
	}

	steps, err := s.ListStepsByRun(ctx, namespace.ID, claimed.Run.ID)
	if err != nil {
		t.Fatalf("ListStepsByRun() error = %v", err)
	}
	if len(steps) != 1 || steps[0].StepIndex != 1 {
		t.Fatalf("ListStepsByRun() = %#v, want one step", steps)
	}
	if steps[0].StepType != "tool" {
		t.Fatalf("ListStepsByRun()[0].StepType = %q, want %q", steps[0].StepType, "tool")
	}
	if steps[0].ActionName != "read_file" {
		t.Fatalf("ListStepsByRun()[0].ActionName = %q, want %q", steps[0].ActionName, "read_file")
	}
	if steps[0].ActionToolKind != "function" {
		t.Fatalf("ListStepsByRun()[0].ActionToolKind = %q, want %q", steps[0].ActionToolKind, "function")
	}
	if steps[0].ActionInput != `{"file_path":"config.yaml"}` {
		t.Fatalf("ListStepsByRun()[0].ActionInput = %q, want config input", steps[0].ActionInput)
	}
	if !strings.Contains(steps[0].ActionOutput, "enabled: true") {
		t.Fatalf("ListStepsByRun()[0].ActionOutput = %q, want tool output", steps[0].ActionOutput)
	}
	if !steps[0].ActionOutputTruncated {
		t.Fatal("ListStepsByRun()[0].ActionOutputTruncated = false, want true")
	}
}

func TestOpenReadOnlyDoesNotCreateDatabase(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "missing.db")
	_, err := OpenReadOnly(context.Background(), dbPath)
	if err == nil {
		t.Fatal("OpenReadOnly() error = nil, want failure for missing database")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("OpenReadOnly() error = %v, want missing database message", err)
	}
	if _, err := os.Stat(dbPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("os.Stat(%q) error = %v, want not-exist", dbPath, err)
	}
}
