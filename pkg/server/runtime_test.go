package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/richardartoul/swarmd/pkg/agent"
	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

func TestRuntimeManagerProcessesMailboxAndOutbox(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-runtime")
	createServerWorkerWithConfig(t, ctx, s, namespace.ID, "worker-a", filepath.Join(t.TempDir(), "worker-a"), nil, map[string]any{
		"capabilities": map[string]any{
			capabilityAllowMessageSend: true,
		},
	})
	createServerWorker(t, ctx, s, namespace.ID, "worker-b", filepath.Join(t.TempDir(), "worker-b"))

	drivers := map[string]*scriptedDriver{
		namespace.ID + ":worker-a": {
			decisions: []agent.Decision{
				withThought(shell(`printf 'hello from a'`), "emit output"),
				{Finish: &agent.FinishAction{Value: map[string]any{
					"reply": "done",
					"outbox": []map[string]any{{
						"recipient_agent_id": "worker-b",
						"payload":            "ping from a",
					}},
				}}},
			},
		},
		namespace.ID + ":worker-b": {
			decisions: []agent.Decision{
				{Finish: &agent.FinishAction{Value: "ack"}},
			},
		},
	}

	var logStdout, logStderr bytes.Buffer
	manager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
			driver, ok := drivers[record.NamespaceID+":"+record.ID]
			if !ok {
				return nil, fmt.Errorf("missing scripted driver for %s:%s", record.NamespaceID, record.ID)
			}
			return driver, nil
		}),
		PollInterval: 50 * time.Millisecond,
		Logger:       NewRuntimeLogger(&logStdout, &logStderr),
	}

	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- manager.Run(runCtx)
	}()

	if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-runtime",
		RecipientAgentID: "worker-a",
		Kind:             "user.prompt",
		Payload:          "start",
		MaxAttempts:      3,
	}); err != nil {
		t.Fatalf("EnqueueMessage() error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() (bool, error) {
		snapshot, err := s.SnapshotNamespace(ctx, namespace.ID)
		if err != nil {
			return false, err
		}
		return snapshot.Mailbox.Completed == 2, nil
	})

	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("RuntimeManager.Run() error = %v, want %v", err, context.Canceled)
	}

	var stepCount int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM steps WHERE namespace_id = ?`, namespace.ID).Scan(&stepCount); err != nil {
		t.Fatalf("count steps error = %v", err)
	}
	if stepCount != 1 {
		t.Fatalf("step count = %d, want 1", stepCount)
	}

	threadMessages, err := s.ListThreadMessages(ctx, namespace.ID, "thread-runtime", 10)
	if err != nil {
		t.Fatalf("ListThreadMessages() error = %v", err)
	}
	if len(threadMessages) != 2 {
		t.Fatalf("len(threadMessages) = %d, want 2", len(threadMessages))
	}
	if threadMessages[1].RecipientAgentID != "worker-b" {
		t.Fatalf("outbox recipient = %q, want %q", threadMessages[1].RecipientAgentID, "worker-b")
	}
	if got := logStdout.String(); !strings.Contains(got, "run> "+namespace.ID+"/worker-a") {
		t.Fatalf("log stdout = %q, want run start log", got)
	}
	if got := logStdout.String(); !strings.Contains(got, "finished status=finished") {
		t.Fatalf("log stdout = %q, want finished run log", got)
	}
}

func TestRuntimeManagerKeepsNamespacesIsolated(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespaceOne := createServerNamespace(t, ctx, s, "namespace-one")
	namespaceTwo := createServerNamespace(t, ctx, s, "namespace-two")

	createServerWorker(t, ctx, s, namespaceOne.ID, "worker", filepath.Join(t.TempDir(), "namespace-one-worker"))
	createServerWorker(t, ctx, s, namespaceTwo.ID, "worker", filepath.Join(t.TempDir(), "namespace-two-worker"))

	manager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
			return &scriptedDriver{
				decisions: []agent.Decision{
					{Finish: &agent.FinishAction{Value: record.NamespaceID}},
				},
			}, nil
		}),
		PollInterval: 50 * time.Millisecond,
	}

	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- manager.Run(runCtx)
	}()

	if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      namespaceOne.ID,
		ThreadID:         "thread-isolation",
		RecipientAgentID: "worker",
		Payload:          "hello namespace one",
	}); err != nil {
		t.Fatalf("EnqueueMessage() error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() (bool, error) {
		snapshot, err := s.SnapshotNamespace(ctx, namespaceOne.ID)
		if err != nil {
			return false, err
		}
		return snapshot.Mailbox.Completed == 1, nil
	})

	time.Sleep(150 * time.Millisecond)

	snapshotTwo, err := s.SnapshotNamespace(ctx, namespaceTwo.ID)
	if err != nil {
		t.Fatalf("SnapshotNamespace() for namespace two error = %v", err)
	}
	if snapshotTwo.Mailbox.Completed != 0 {
		t.Fatalf("namespace two completed mailbox count = %d, want 0", snapshotTwo.Mailbox.Completed)
	}

	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("RuntimeManager.Run() error = %v, want %v", err, context.Canceled)
	}
}

func TestRuntimeManagerStoresRunPromptSnapshots(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-prompts")
	record := createServerWorker(t, ctx, s, namespace.ID, "worker", filepath.Join(t.TempDir(), "worker"))

	manager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, agentRecord cpstore.RunnableAgent) (agent.Driver, error) {
			if agentRecord.NamespaceID != namespace.ID || agentRecord.ID != record.ID {
				return nil, fmt.Errorf("unexpected worker %s/%s", agentRecord.NamespaceID, agentRecord.ID)
			}
			return &scriptedDriver{
				decisions: []agent.Decision{
					{Finish: &agent.FinishAction{Value: "done"}},
				},
			}, nil
		}),
		PollInterval: 50 * time.Millisecond,
	}

	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- manager.Run(runCtx)
	}()

	if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-prompts",
		RecipientAgentID: record.ID,
		Kind:             "user.prompt",
		Payload:          "  prompt from runtime  ",
		MaxAttempts:      3,
	}); err != nil {
		t.Fatalf("EnqueueMessage() error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() (bool, error) {
		snapshot, err := s.SnapshotNamespace(ctx, namespace.ID)
		if err != nil {
			return false, err
		}
		return snapshot.Mailbox.Completed == 1, nil
	})

	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("RuntimeManager.Run() error = %v, want %v", err, context.Canceled)
	}

	runs, err := s.ListRuns(ctx, cpstore.ListRunsParams{
		NamespaceID: namespace.ID,
		AgentID:     record.ID,
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("len(ListRuns()) = %d, want 1", len(runs))
	}
	if runs[0].TriggerPrompt != "prompt from runtime" {
		t.Fatalf("run trigger prompt = %q, want %q", runs[0].TriggerPrompt, "prompt from runtime")
	}

	expectedSystemPrompt := composeManagedSystemPrompt(record, nil, AgentMemorySpec{}, nil, managedAgentNetworkConfig{}, nil)
	if runs[0].SystemPrompt != expectedSystemPrompt {
		t.Fatalf("run system prompt = %q, want %q", runs[0].SystemPrompt, expectedSystemPrompt)
	}
}

func TestRuntimeManagerSweepsStaleSpillDirsOnWorkerStart(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-spill-sweep")
	root := filepath.Join(t.TempDir(), "worker")
	record := createServerWorker(t, ctx, s, namespace.ID, "worker", root)

	staleDir := filepath.Join(root, ".tmp", "tool-outputs", "stale-run", "step_1")
	if err := os.MkdirAll(staleDir, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(staleDir) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(staleDir, "leftover.txt"), []byte("leftover"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(leftover.txt) error = %v", err)
	}

	manager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, agentRecord cpstore.RunnableAgent) (agent.Driver, error) {
			if agentRecord.NamespaceID != namespace.ID || agentRecord.ID != record.ID {
				return nil, fmt.Errorf("unexpected worker %s/%s", agentRecord.NamespaceID, agentRecord.ID)
			}
			return &scriptedDriver{
				decisions: []agent.Decision{
					{Finish: &agent.FinishAction{Value: "done"}},
				},
			}, nil
		}),
		PollInterval: 50 * time.Millisecond,
	}

	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- manager.Run(runCtx)
	}()

	waitForCondition(t, 5*time.Second, func() (bool, error) {
		_, err := os.Stat(filepath.Join(root, ".tmp", "tool-outputs"))
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		return false, err
	})

	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("RuntimeManager.Run() error = %v, want %v", err, context.Canceled)
	}
}

func TestResultPersisterRetriesThenDeadLetters(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-retry")
	createServerWorker(t, ctx, s, namespace.ID, "worker", filepath.Join(t.TempDir(), "worker"))

	if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-retry",
		RecipientAgentID: "worker",
		Payload:          "retry me",
		MaxAttempts:      2,
	}); err != nil {
		t.Fatalf("EnqueueMessage() error = %v", err)
	}

	queue := &MessageQueue{
		Store:         s,
		NamespaceID:   namespace.ID,
		AgentID:       "worker",
		PollInterval:  10 * time.Millisecond,
		LeaseDuration: time.Minute,
	}
	persister := ResultPersister{
		Store:      s,
		RetryDelay: 10 * time.Millisecond,
	}

	firstTrigger, err := queue.Next(ctx)
	if err != nil {
		t.Fatalf("queue.Next() first error = %v", err)
	}
	if err := persister.HandleResult(ctx, agent.Result{
		Trigger:    firstTrigger,
		FinishedAt: time.Now().UTC(),
		Duration:   5 * time.Millisecond,
		Status:     agent.ResultStatusFatalError,
		Error:      "boom",
	}); err != nil {
		t.Fatalf("HandleResult() first error = %v", err)
	}

	time.Sleep(20 * time.Millisecond)

	secondTrigger, err := queue.Next(ctx)
	if err != nil {
		t.Fatalf("queue.Next() second error = %v", err)
	}
	if err := persister.HandleResult(ctx, agent.Result{
		Trigger:    secondTrigger,
		FinishedAt: time.Now().UTC(),
		Duration:   5 * time.Millisecond,
		Status:     agent.ResultStatusFatalError,
		Error:      "boom again",
	}); err != nil {
		t.Fatalf("HandleResult() second error = %v", err)
	}

	snapshot, err := s.SnapshotNamespace(ctx, namespace.ID)
	if err != nil {
		t.Fatalf("SnapshotNamespace() error = %v", err)
	}
	if snapshot.Mailbox.DeadLetter != 1 {
		t.Fatalf("dead-letter mailbox count = %d, want 1", snapshot.Mailbox.DeadLetter)
	}
}

func TestResultPersisterStoresFinishThought(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-finish-thought")
	createServerWorker(t, ctx, s, namespace.ID, "worker", filepath.Join(t.TempDir(), "worker"))

	if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-finish-thought",
		RecipientAgentID: "worker",
		Payload:          "say done",
		MaxAttempts:      1,
	}); err != nil {
		t.Fatalf("EnqueueMessage() error = %v", err)
	}

	queue := &MessageQueue{
		Store:         s,
		NamespaceID:   namespace.ID,
		AgentID:       "worker",
		PollInterval:  10 * time.Millisecond,
		LeaseDuration: time.Minute,
	}
	persister := ResultPersister{Store: s}

	trigger, err := queue.Next(ctx)
	if err != nil {
		t.Fatalf("queue.Next() error = %v", err)
	}
	if err := persister.HandleResult(ctx, agent.Result{
		Trigger:       trigger,
		FinishedAt:    time.Now().UTC(),
		Duration:      5 * time.Millisecond,
		Status:        agent.ResultStatusFinished,
		FinishThought: "all work is complete",
		Value:         "done",
	}); err != nil {
		t.Fatalf("HandleResult() error = %v", err)
	}

	triggerCtx, err := TriggerContextFromTrigger(trigger)
	if err != nil {
		t.Fatalf("TriggerContextFromTrigger() error = %v", err)
	}
	run, err := s.GetRun(ctx, namespace.ID, triggerCtx.RunID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if run.FinishThought != "all work is complete" {
		t.Fatalf("run.FinishThought = %q, want %q", run.FinishThought, "all work is complete")
	}
}

func TestResultPersisterRejectsOutboxWithoutCapability(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-outbox-capability")
	createServerWorker(t, ctx, s, namespace.ID, "worker", filepath.Join(t.TempDir(), "worker"))

	if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-outbox-capability",
		RecipientAgentID: "worker",
		Payload:          "send a follow-up",
		MaxAttempts:      1,
	}); err != nil {
		t.Fatalf("EnqueueMessage() error = %v", err)
	}

	queue := &MessageQueue{
		Store:         s,
		NamespaceID:   namespace.ID,
		AgentID:       "worker",
		PollInterval:  10 * time.Millisecond,
		LeaseDuration: time.Minute,
	}
	persister := ResultPersister{
		Store: s,
	}

	trigger, err := queue.Next(ctx)
	if err != nil {
		t.Fatalf("queue.Next() error = %v", err)
	}
	err = persister.HandleResult(ctx, agent.Result{
		Trigger:    trigger,
		FinishedAt: time.Now().UTC(),
		Duration:   5 * time.Millisecond,
		Status:     agent.ResultStatusFinished,
		Value: map[string]any{
			"reply": "forwarded",
			"outbox": []map[string]any{{
				"recipient_agent_id": "other-worker",
				"payload":            "hello",
			}},
		},
	})
	if err == nil {
		t.Fatal("HandleResult() error = nil, want capability error")
	}
	if !strings.Contains(err.Error(), capabilityAllowMessageSend) {
		t.Fatalf("HandleResult() error = %v, want missing capability", err)
	}
}

func TestResultPersisterRejectsOutboxToDifferentNamespace(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	senderNamespace := createServerNamespace(t, ctx, s, "namespace-outbox-sender")
	receiverNamespace := createServerNamespace(t, ctx, s, "namespace-outbox-receiver")

	createServerWorkerWithConfig(t, ctx, s, senderNamespace.ID, "sender", filepath.Join(t.TempDir(), "sender"), nil, map[string]any{
		"capabilities": map[string]any{
			capabilityAllowMessageSend: true,
		},
	})
	createServerWorker(t, ctx, s, receiverNamespace.ID, "receiver", filepath.Join(t.TempDir(), "receiver"))

	if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      senderNamespace.ID,
		ThreadID:         "thread-outbox-cross-namespace",
		RecipientAgentID: "sender",
		Payload:          "send to another namespace",
		MaxAttempts:      1,
	}); err != nil {
		t.Fatalf("EnqueueMessage() error = %v", err)
	}

	queue := &MessageQueue{
		Store:         s,
		NamespaceID:   senderNamespace.ID,
		AgentID:       "sender",
		PollInterval:  10 * time.Millisecond,
		LeaseDuration: time.Minute,
	}
	persister := ResultPersister{
		Store:            s,
		AllowMessageSend: true,
	}

	trigger, err := queue.Next(ctx)
	if err != nil {
		t.Fatalf("queue.Next() error = %v", err)
	}
	err = persister.HandleResult(ctx, agent.Result{
		Trigger:    trigger,
		FinishedAt: time.Now().UTC(),
		Duration:   5 * time.Millisecond,
		Status:     agent.ResultStatusFinished,
		Value: map[string]any{
			"reply": "forwarded",
			"outbox": []map[string]any{{
				"recipient_agent_id": "receiver",
				"payload":            "hello",
			}},
		},
	})
	if err == nil {
		t.Fatal("HandleResult() error = nil, want namespace-scoped recipient failure")
	}

	snapshot, err := s.SnapshotNamespace(ctx, receiverNamespace.ID)
	if err != nil {
		t.Fatalf("SnapshotNamespace() error = %v", err)
	}
	if snapshot.Mailbox.Queued != 0 || snapshot.Mailbox.Leased != 0 || snapshot.Mailbox.Completed != 0 {
		t.Fatalf("receiver namespace mailbox = %#v, want no delivered message", snapshot.Mailbox)
	}
}

func TestRuntimeManagerLogsDriverErrors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-errors")
	createServerWorker(t, ctx, s, namespace.ID, "worker", filepath.Join(t.TempDir(), "worker"))

	var logStdout, logStderr bytes.Buffer
	manager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
			return agent.DriverFunc(func(context.Context, agent.Request) (agent.Decision, error) {
				return agent.Decision{}, fmt.Errorf("synthetic driver failure")
			}), nil
		}),
		PollInterval: 50 * time.Millisecond,
		Logger:       NewRuntimeLogger(&logStdout, &logStderr),
	}

	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- manager.Run(runCtx)
	}()

	if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-errors",
		RecipientAgentID: "worker",
		Payload:          "break",
		MaxAttempts:      1,
	}); err != nil {
		t.Fatalf("EnqueueMessage() error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() (bool, error) {
		snapshot, err := s.SnapshotNamespace(ctx, namespace.ID)
		if err != nil {
			return false, err
		}
		return snapshot.Mailbox.DeadLetter == 1, nil
	})

	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("RuntimeManager.Run() error = %v, want %v", err, context.Canceled)
	}
	if got := logStdout.String(); !strings.Contains(got, "run> "+namespace.ID+"/worker") {
		t.Fatalf("log stdout = %q, want run start log", got)
	}
	if got := logStderr.String(); !strings.Contains(got, "status=driver_error") {
		t.Fatalf("log stderr = %q, want driver_error log", got)
	}
	if got := logStderr.String(); !strings.Contains(got, "synthetic driver failure") {
		t.Fatalf("log stderr = %q, want driver error text", got)
	}
}

func TestRuntimeManagerComposesBasePromptWithConfiguredPrompt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-prompt")
	createServerWorkerWithCommands(t, ctx, s, namespace.ID, "worker", filepath.Join(t.TempDir(), "worker"), []string{"server_log"})

	if _, err := s.UpdateAgentPrompt(ctx, cpstore.UpdateAgentPromptParams{
		NamespaceID: namespace.ID,
		AgentID:     "worker",
		Prompt:      "Run curl and summarize the result.",
	}); err != nil {
		t.Fatalf("UpdateAgentPrompt() error = %v", err)
	}

	var seenPrompts []string
	manager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
			return agent.DriverFunc(func(_ context.Context, req agent.Request) (agent.Decision, error) {
				if len(req.Messages) == 0 {
					t.Fatal("driver request had no messages")
				}
				for _, message := range req.Messages {
					if message.Role == agent.MessageRoleSystem {
						seenPrompts = append(seenPrompts, message.Content)
					}
				}
				return agent.Decision{Finish: &agent.FinishAction{Value: "ok"}}, nil
			}), nil
		}),
		PollInterval: 50 * time.Millisecond,
	}

	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- manager.Run(runCtx)
	}()

	if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-prompt",
		RecipientAgentID: "worker",
		Payload:          "start",
		MaxAttempts:      1,
	}); err != nil {
		t.Fatalf("EnqueueMessage() error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() (bool, error) {
		return len(seenPrompts) > 0, nil
	})

	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("RuntimeManager.Run() error = %v, want %v", err, context.Canceled)
	}
	combinedPrompts := strings.Join(seenPrompts, "\n")
	if !strings.Contains(combinedPrompts, `Use the structured tools provided by the runtime as the source of truth`) {
		t.Fatalf("combined system prompts = %q, want base tool-aware instructions", combinedPrompts)
	}
	if !strings.Contains(combinedPrompts, `Run curl and summarize the result.`) {
		t.Fatalf("combined system prompts = %q, want configured prompt", combinedPrompts)
	}
	if !strings.Contains(combinedPrompts, `Runtime-only run_shell guidance:`) {
		t.Fatalf("combined system prompts = %q, want runtime-only run_shell guidance", combinedPrompts)
	}
}

func TestRuntimeManagerDoesNotInjectMailboxThreadHistory(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-no-history")
	createServerWorker(t, ctx, s, namespace.ID, "worker", filepath.Join(t.TempDir(), "worker"))

	var (
		requestsMu sync.Mutex
		requests   []agent.Request
	)
	manager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
			return agent.DriverFunc(func(_ context.Context, req agent.Request) (agent.Decision, error) {
				requestsMu.Lock()
				requests = append(requests, req)
				requestsMu.Unlock()
				return agent.Decision{Finish: &agent.FinishAction{Value: "ok"}}, nil
			}), nil
		}),
		PollInterval: 50 * time.Millisecond,
	}

	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- manager.Run(runCtx)
	}()

	if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-no-history",
		RecipientAgentID: "worker",
		Payload:          "first payload",
		MaxAttempts:      1,
	}); err != nil {
		t.Fatalf("EnqueueMessage(first) error = %v", err)
	}
	waitForCondition(t, 5*time.Second, func() (bool, error) {
		snapshot, err := s.SnapshotNamespace(ctx, namespace.ID)
		if err != nil {
			return false, err
		}
		return snapshot.Mailbox.Completed == 1, nil
	})

	if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-no-history",
		RecipientAgentID: "worker",
		Payload:          "second payload",
		MaxAttempts:      1,
	}); err != nil {
		t.Fatalf("EnqueueMessage(second) error = %v", err)
	}
	waitForCondition(t, 5*time.Second, func() (bool, error) {
		snapshot, err := s.SnapshotNamespace(ctx, namespace.ID)
		if err != nil {
			return false, err
		}
		return snapshot.Mailbox.Completed == 2, nil
	})

	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("RuntimeManager.Run() error = %v, want %v", err, context.Canceled)
	}

	requestsMu.Lock()
	defer requestsMu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("len(requests) = %d, want 2", len(requests))
	}
	var secondRequest strings.Builder
	for _, message := range requests[1].Messages {
		secondRequest.WriteString(message.Content)
		secondRequest.WriteString("\n")
	}
	if got := secondRequest.String(); strings.Contains(got, "Durable mailbox thread history") {
		t.Fatalf("second request = %q, want no injected mailbox history", got)
	}
	if got := secondRequest.String(); strings.Contains(got, "first payload") {
		t.Fatalf("second request = %q, want no previous payload replay", got)
	}
}

func TestRuntimeManagerAddsDefaultMemoryGuidanceWithoutCreatingRootFile(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-memory-default")
	root := filepath.Join(t.TempDir(), "worker")
	createServerWorker(t, ctx, s, namespace.ID, "worker", root)

	var (
		promptsMu sync.Mutex
		prompts   []string
	)
	manager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
			return agent.DriverFunc(func(_ context.Context, req agent.Request) (agent.Decision, error) {
				promptsMu.Lock()
				for _, message := range req.Messages {
					if message.Role == agent.MessageRoleSystem {
						prompts = append(prompts, message.Content)
					}
				}
				promptsMu.Unlock()
				return agent.Decision{Finish: &agent.FinishAction{Value: "ok"}}, nil
			}), nil
		}),
		PollInterval: 50 * time.Millisecond,
	}

	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- manager.Run(runCtx)
	}()

	if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-memory-default",
		RecipientAgentID: "worker",
		Payload:          "start",
		MaxAttempts:      1,
	}); err != nil {
		t.Fatalf("EnqueueMessage() error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() (bool, error) {
		promptsMu.Lock()
		defer promptsMu.Unlock()
		return len(prompts) > 0, nil
	})

	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("RuntimeManager.Run() error = %v, want %v", err, context.Canceled)
	}

	promptsMu.Lock()
	combinedPrompts := strings.Join(prompts, "\n")
	promptsMu.Unlock()
	if !strings.Contains(combinedPrompts, defaultAgentMemoryRootRelativePath()) {
		t.Fatalf("combined system prompts = %q, want default memory root path", combinedPrompts)
	}
	if !strings.Contains(combinedPrompts, "If "+defaultAgentMemoryRootRelativePath()+" exists, read it at the start of each run") {
		t.Fatalf("combined system prompts = %q, want lazy memory-read guidance", combinedPrompts)
	}
	if !strings.Contains(combinedPrompts, "Reading too much memory at once can pollute your context") {
		t.Fatalf("combined system prompts = %q, want memory-discipline warning", combinedPrompts)
	}

	rootFile := filepath.Join(root, defaultAgentMemoryDir, defaultAgentMemoryRootFile)
	if _, err := os.Stat(rootFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("os.Stat(%q) error = %v, want no auto-created root file", rootFile, err)
	}
}

func TestRuntimeManagerRespectsMemoryDisableAndPromptOverride(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		config          map[string]any
		wantInPrompt    string
		wantNotInPrompt string
	}{
		{
			name: "disabled",
			config: map[string]any{
				"managed_by":  "filesystem",
				"source_path": "test.yaml",
				"memory": map[string]any{
					"disable": true,
				},
			},
			wantNotInPrompt: defaultAgentMemoryRootRelativePath(),
		},
		{
			name: "prompt override",
			config: map[string]any{
				"managed_by":  "filesystem",
				"source_path": "test.yaml",
				"memory": map[string]any{
					"prompt_override": "Always start with .memory/ROOT.md and only load URNs that match the current task.",
				},
			},
			wantInPrompt:    "Always start with .memory/ROOT.md and only load URNs that match the current task.",
			wantNotInPrompt: "Reading too much memory at once can pollute your context",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := newServerStore(t)
			namespace := createServerNamespace(t, ctx, s, "namespace-"+tt.name)
			root := filepath.Join(t.TempDir(), "worker")
			createServerWorkerWithConfig(t, ctx, s, namespace.ID, "worker", root, nil, tt.config)

			var (
				promptsMu sync.Mutex
				prompts   []string
			)
			manager := &RuntimeManager{
				Store: s,
				DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
					return agent.DriverFunc(func(_ context.Context, req agent.Request) (agent.Decision, error) {
						promptsMu.Lock()
						for _, message := range req.Messages {
							if message.Role == agent.MessageRoleSystem {
								prompts = append(prompts, message.Content)
							}
						}
						promptsMu.Unlock()
						return agent.Decision{Finish: &agent.FinishAction{Value: "ok"}}, nil
					}), nil
				}),
				PollInterval: 50 * time.Millisecond,
			}

			runCtx, cancel := context.WithCancel(context.Background())
			errCh := make(chan error, 1)
			go func() {
				errCh <- manager.Run(runCtx)
			}()

			if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
				NamespaceID:      namespace.ID,
				ThreadID:         "thread-" + tt.name,
				RecipientAgentID: "worker",
				Payload:          "start",
				MaxAttempts:      1,
			}); err != nil {
				t.Fatalf("EnqueueMessage() error = %v", err)
			}

			waitForCondition(t, 5*time.Second, func() (bool, error) {
				promptsMu.Lock()
				defer promptsMu.Unlock()
				return len(prompts) > 0, nil
			})

			cancel()
			if err := <-errCh; !errors.Is(err, context.Canceled) {
				t.Fatalf("RuntimeManager.Run() error = %v, want %v", err, context.Canceled)
			}

			promptsMu.Lock()
			combinedPrompts := strings.Join(prompts, "\n")
			promptsMu.Unlock()
			if tt.wantInPrompt != "" && !strings.Contains(combinedPrompts, tt.wantInPrompt) {
				t.Fatalf("combined system prompts = %q, want %q", combinedPrompts, tt.wantInPrompt)
			}
			if tt.wantNotInPrompt != "" && strings.Contains(combinedPrompts, tt.wantNotInPrompt) {
				t.Fatalf("combined system prompts = %q, do not want %q", combinedPrompts, tt.wantNotInPrompt)
			}
		})
	}
}

func TestRuntimeManagerMemoryFilesPersistAcrossRestarts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-memory-persist")
	root := filepath.Join(t.TempDir(), "worker")
	createServerWorker(t, ctx, s, namespace.ID, "worker", root)

	firstManager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
			return &scriptedDriver{
				decisions: []agent.Decision{
					withThought(shell("mkdir -p .memory/topics && printf '## URN Index\n- urn:test:topic -> topics/test.md\n' > .memory/ROOT.md && printf 'first note\n' > .memory/topics/test.md"), "write memory files"),
					{Finish: &agent.FinishAction{Value: "seeded"}},
				},
			}, nil
		}),
		PollInterval: 50 * time.Millisecond,
	}

	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- firstManager.Run(runCtx)
	}()

	if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-memory-first",
		RecipientAgentID: "worker",
		Payload:          "seed memory",
		MaxAttempts:      1,
	}); err != nil {
		t.Fatalf("EnqueueMessage(first) error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() (bool, error) {
		snapshot, err := s.SnapshotNamespace(ctx, namespace.ID)
		if err != nil {
			return false, err
		}
		return snapshot.Mailbox.Completed == 1, nil
	})

	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("first RuntimeManager.Run() error = %v, want %v", err, context.Canceled)
	}

	secondManager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
			return &scriptedDriver{
				decisions: []agent.Decision{
					withThought(shell("cat .memory/ROOT.md .memory/topics/test.md > .memory/readback.txt && printf 'second note\n' >> .memory/topics/test.md"), "read root index and update topic"),
					{Finish: &agent.FinishAction{Value: "updated"}},
				},
			}, nil
		}),
		PollInterval: 50 * time.Millisecond,
	}

	runCtx, cancel = context.WithCancel(context.Background())
	errCh = make(chan error, 1)
	go func() {
		errCh <- secondManager.Run(runCtx)
	}()

	if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-memory-second",
		RecipientAgentID: "worker",
		Payload:          "reuse memory",
		MaxAttempts:      1,
	}); err != nil {
		t.Fatalf("EnqueueMessage(second) error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() (bool, error) {
		snapshot, err := s.SnapshotNamespace(ctx, namespace.ID)
		if err != nil {
			return false, err
		}
		return snapshot.Mailbox.Completed == 2, nil
	})

	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("second RuntimeManager.Run() error = %v, want %v", err, context.Canceled)
	}

	readbackPath := filepath.Join(root, defaultAgentMemoryDir, "readback.txt")
	readback, err := os.ReadFile(readbackPath)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error = %v", readbackPath, err)
	}
	if got := string(readback); !strings.Contains(got, "urn:test:topic") || !strings.Contains(got, "first note") {
		t.Fatalf("readback file = %q, want root index and first note", got)
	}

	topicPath := filepath.Join(root, defaultAgentMemoryDir, "topics", "test.md")
	topic, err := os.ReadFile(topicPath)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error = %v", topicPath, err)
	}
	if got := string(topic); !strings.Contains(got, "first note") || !strings.Contains(got, "second note") {
		t.Fatalf("topic file = %q, want notes from both runs", got)
	}
}

func TestRuntimeManagerMaterializesMountedFilesBeforeRun(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-mounted-files")
	sourcePath := filepath.Join(t.TempDir(), "source", "message.txt")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", filepath.Dir(sourcePath), err)
	}
	if err := os.WriteFile(sourcePath, []byte("mounted hello\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", sourcePath, err)
	}
	root := filepath.Join(t.TempDir(), "worker")
	createServerWorkerWithConfig(t, ctx, s, namespace.ID, "worker", root, nil, managedAgentRuntimeConfig{
		Mounts: []managedAgentMount{
			{
				Path: "mounted/message.txt",
				Source: managedAgentMountSource{
					Path:         sourcePath,
					ResolvedPath: sourcePath,
				},
				Kind: managedAgentMountKindFile,
			},
			{
				Path:          "mounted/show.sh",
				Kind:          managedAgentMountKindFile,
				ContentBase64: base64.StdEncoding.EncodeToString([]byte("cat mounted/message.txt\n")),
			},
		},
	})

	manager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
			return &scriptedDriver{
				decisions: []agent.Decision{
					withThought(shell("cat mounted/message.txt > mounted/from-direct.txt && sh mounted/show.sh > mounted/from-nested.txt"), "read mounted files"),
					{Finish: &agent.FinishAction{Value: "mounted"}},
				},
			}, nil
		}),
		PollInterval: 50 * time.Millisecond,
	}

	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- manager.Run(runCtx)
	}()

	if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-mounted-files",
		RecipientAgentID: "worker",
		Payload:          "use mounts",
		MaxAttempts:      1,
	}); err != nil {
		t.Fatalf("EnqueueMessage() error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() (bool, error) {
		snapshot, err := s.SnapshotNamespace(ctx, namespace.ID)
		if err != nil {
			return false, err
		}
		return snapshot.Mailbox.Completed == 1, nil
	})

	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("RuntimeManager.Run() error = %v, want %v", err, context.Canceled)
	}

	direct, err := os.ReadFile(filepath.Join(root, "mounted", "from-direct.txt"))
	if err != nil {
		t.Fatalf("os.ReadFile(from-direct) error = %v", err)
	}
	if got := string(direct); got != "mounted hello\n" {
		t.Fatalf("from-direct.txt = %q, want %q", got, "mounted hello\n")
	}
	nested, err := os.ReadFile(filepath.Join(root, "mounted", "from-nested.txt"))
	if err != nil {
		t.Fatalf("os.ReadFile(from-nested) error = %v", err)
	}
	if got := string(nested); got != "mounted hello\n" {
		t.Fatalf("from-nested.txt = %q, want %q", got, "mounted hello\n")
	}
}

func TestRuntimeManagerMaterializesEnvBackedMountsBeforeRun(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-mounted-env-files")
	root := filepath.Join(t.TempDir(), "worker")
	createServerWorkerWithConfig(t, ctx, s, namespace.ID, "worker", root, nil, managedAgentRuntimeConfig{
		Mounts: []managedAgentMount{{
			Path: "mounted/secret.txt",
			Source: managedAgentMountSource{
				EnvVar: "EXAMPLE_API_KEY",
			},
			Kind: managedAgentMountKindFile,
		}},
	})

	manager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
			return &scriptedDriver{
				decisions: []agent.Decision{
					withThought(shell("cat mounted/secret.txt > mounted/readback.txt"), "read mounted secret"),
					{Finish: &agent.FinishAction{Value: "mounted"}},
				},
			}, nil
		}),
		PollInterval: 50 * time.Millisecond,
		EnvLookup: func(key string) string {
			if key == "EXAMPLE_API_KEY" {
				return "top-secret"
			}
			return ""
		},
	}

	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- manager.Run(runCtx)
	}()

	if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-mounted-env-files",
		RecipientAgentID: "worker",
		Payload:          "use env mount",
		MaxAttempts:      1,
	}); err != nil {
		t.Fatalf("EnqueueMessage() error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() (bool, error) {
		snapshot, err := s.SnapshotNamespace(ctx, namespace.ID)
		if err != nil {
			return false, err
		}
		return snapshot.Mailbox.Completed == 1, nil
	})

	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("RuntimeManager.Run() error = %v, want %v", err, context.Canceled)
	}

	secret, err := os.ReadFile(filepath.Join(root, "mounted", "readback.txt"))
	if err != nil {
		t.Fatalf("os.ReadFile(readback.txt) error = %v", err)
	}
	if got := string(secret); got != "top-secret" {
		t.Fatalf("readback.txt = %q, want %q", got, "top-secret")
	}
}

func TestRuntimeManagerMaterializesMountedDirectoriesAndOverwritesExistingTargets(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-mounted-directories")
	sourceRoot := filepath.Join(t.TempDir(), "source")
	assetsSource := filepath.Join(sourceRoot, "assets")
	fileSource := filepath.Join(sourceRoot, "file.txt")
	if err := os.MkdirAll(filepath.Join(assetsSource, "templates"), 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", filepath.Join(assetsSource, "templates"), err)
	}
	if err := os.WriteFile(filepath.Join(assetsSource, "templates", "base.txt"), []byte("base template\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(base.txt) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(assetsSource, "notes.txt"), []byte("directory note\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(notes.txt) error = %v", err)
	}
	if err := os.WriteFile(fileSource, []byte("fresh file\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", fileSource, err)
	}
	root := filepath.Join(t.TempDir(), "worker")
	if err := os.MkdirAll(filepath.Join(root, "mounted"), 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", filepath.Join(root, "mounted"), err)
	}
	if err := os.WriteFile(filepath.Join(root, "mounted", "assets"), []byte("stale file"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(assets) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "mounted", "file.txt"), []byte("stale file"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(file.txt) error = %v", err)
	}
	createServerWorkerWithConfig(t, ctx, s, namespace.ID, "worker", root, nil, managedAgentRuntimeConfig{
		Mounts: []managedAgentMount{
			{
				Path: "mounted/assets",
				Source: managedAgentMountSource{
					Path:         assetsSource,
					ResolvedPath: assetsSource,
				},
				Kind: managedAgentMountKindDirectory,
			},
			{
				Path: "mounted/file.txt",
				Source: managedAgentMountSource{
					Path:         fileSource,
					ResolvedPath: fileSource,
				},
				Kind: managedAgentMountKindFile,
			},
		},
	})

	manager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
			return &scriptedDriver{
				decisions: []agent.Decision{
					withThought(shell("cat mounted/file.txt mounted/assets/notes.txt mounted/assets/templates/base.txt > mounted/combined.txt"), "read mounted directory and file"),
					{Finish: &agent.FinishAction{Value: "mounted"}},
				},
			}, nil
		}),
		PollInterval: 50 * time.Millisecond,
	}

	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- manager.Run(runCtx)
	}()

	if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-mounted-directories",
		RecipientAgentID: "worker",
		Payload:          "use directory mount",
		MaxAttempts:      1,
	}); err != nil {
		t.Fatalf("EnqueueMessage() error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() (bool, error) {
		snapshot, err := s.SnapshotNamespace(ctx, namespace.ID)
		if err != nil {
			return false, err
		}
		return snapshot.Mailbox.Completed == 1, nil
	})

	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("RuntimeManager.Run() error = %v, want %v", err, context.Canceled)
	}

	assetsInfo, err := os.Stat(filepath.Join(root, "mounted", "assets"))
	if err != nil {
		t.Fatalf("os.Stat(assets) error = %v", err)
	}
	if !assetsInfo.IsDir() {
		t.Fatalf("mounted/assets IsDir = false, want true")
	}
	combined, err := os.ReadFile(filepath.Join(root, "mounted", "combined.txt"))
	if err != nil {
		t.Fatalf("os.ReadFile(combined.txt) error = %v", err)
	}
	if got := string(combined); got != "fresh file\ndirectory note\nbase template\n" {
		t.Fatalf("combined.txt = %q, want mounted directory and overwritten file contents", got)
	}
}

func TestRuntimeManagerAddsMountGuidanceToSystemPrompt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-mount-prompt")
	sourceRoot := filepath.Join(t.TempDir(), "source")
	contextSource := filepath.Join(sourceRoot, "context.txt")
	templatesSource := filepath.Join(sourceRoot, "templates")
	if err := os.MkdirAll(templatesSource, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", templatesSource, err)
	}
	if err := os.WriteFile(contextSource, []byte("context"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", contextSource, err)
	}
	if err := os.WriteFile(filepath.Join(templatesSource, "summary.md"), []byte("template"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(summary.md) error = %v", err)
	}
	root := filepath.Join(t.TempDir(), "worker")
	createServerWorkerWithConfig(t, ctx, s, namespace.ID, "worker", root, nil, managedAgentRuntimeConfig{
		Mounts: []managedAgentMount{
			{
				Path:        "mounted/context.txt",
				Description: "Current run context file.",
				Source: managedAgentMountSource{
					Path:         contextSource,
					ResolvedPath: contextSource,
				},
				Kind: managedAgentMountKindFile,
			},
			{
				Path:        "mounted/templates",
				Description: "Reusable report templates.",
				Source: managedAgentMountSource{
					Path:         templatesSource,
					ResolvedPath: templatesSource,
				},
				Kind: managedAgentMountKindDirectory,
			},
		},
	})

	var (
		promptsMu sync.Mutex
		prompts   []string
	)
	manager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
			return agent.DriverFunc(func(_ context.Context, req agent.Request) (agent.Decision, error) {
				promptsMu.Lock()
				for _, message := range req.Messages {
					if message.Role == agent.MessageRoleSystem {
						prompts = append(prompts, message.Content)
					}
				}
				promptsMu.Unlock()
				return agent.Decision{Finish: &agent.FinishAction{Value: "ok"}}, nil
			}), nil
		}),
		PollInterval: 50 * time.Millisecond,
	}

	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- manager.Run(runCtx)
	}()

	if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-mount-prompt",
		RecipientAgentID: "worker",
		Payload:          "start",
		MaxAttempts:      1,
	}); err != nil {
		t.Fatalf("EnqueueMessage() error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() (bool, error) {
		promptsMu.Lock()
		defer promptsMu.Unlock()
		return len(prompts) > 0, nil
	})

	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("RuntimeManager.Run() error = %v, want %v", err, context.Canceled)
	}

	promptsMu.Lock()
	combinedPrompts := strings.Join(prompts, "\n")
	promptsMu.Unlock()
	if !strings.Contains(combinedPrompts, "Mounted resources:") {
		t.Fatalf("combined system prompts = %q, want mount guidance header", combinedPrompts)
	}
	if !strings.Contains(combinedPrompts, "mounted/context.txt (file): Current run context file.") {
		t.Fatalf("combined system prompts = %q, want file mount description", combinedPrompts)
	}
	if !strings.Contains(combinedPrompts, "mounted/templates (directory): Reusable report templates.") {
		t.Fatalf("combined system prompts = %q, want directory mount description", combinedPrompts)
	}
	if !strings.Contains(combinedPrompts, "sandbox-local copies") {
		t.Fatalf("combined system prompts = %q, want copy semantics guidance", combinedPrompts)
	}
}

func TestRuntimeManagerRefreshesPathMountsOnlyAfterWorkerRestart(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-mounted-refresh")
	sourcePath := filepath.Join(t.TempDir(), "source", "message.txt")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", filepath.Dir(sourcePath), err)
	}
	if err := os.WriteFile(sourcePath, []byte("version one\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", sourcePath, err)
	}
	root := filepath.Join(t.TempDir(), "worker")
	createServerWorkerWithConfig(t, ctx, s, namespace.ID, "worker", root, nil, managedAgentRuntimeConfig{
		Mounts: []managedAgentMount{{
			Path: "mounted/message.txt",
			Source: managedAgentMountSource{
				Path:         sourcePath,
				ResolvedPath: sourcePath,
			},
			Kind: managedAgentMountKindFile,
		}},
	})

	firstManager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
			return &scriptedDriver{
				decisions: []agent.Decision{
					withThought(shell("cat mounted/message.txt > mounted/first.txt"), "read first mounted file snapshot"),
					{Finish: &agent.FinishAction{Value: "first"}},
					withThought(shell("cat mounted/message.txt > mounted/second.txt"), "read second mounted file snapshot"),
					{Finish: &agent.FinishAction{Value: "second"}},
				},
			}, nil
		}),
		PollInterval: 50 * time.Millisecond,
	}

	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- firstManager.Run(runCtx)
	}()

	if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-mounted-refresh-first",
		RecipientAgentID: "worker",
		Payload:          "read first snapshot",
		MaxAttempts:      1,
	}); err != nil {
		t.Fatalf("EnqueueMessage(first) error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() (bool, error) {
		snapshot, err := s.SnapshotNamespace(ctx, namespace.ID)
		if err != nil {
			return false, err
		}
		return snapshot.Mailbox.Completed == 1, nil
	})

	if err := os.WriteFile(sourcePath, []byte("version two\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) update error = %v", sourcePath, err)
	}

	if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-mounted-refresh-second",
		RecipientAgentID: "worker",
		Payload:          "read second snapshot",
		MaxAttempts:      1,
	}); err != nil {
		t.Fatalf("EnqueueMessage(second) error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() (bool, error) {
		snapshot, err := s.SnapshotNamespace(ctx, namespace.ID)
		if err != nil {
			return false, err
		}
		return snapshot.Mailbox.Completed == 2, nil
	})

	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("first RuntimeManager.Run() error = %v, want %v", err, context.Canceled)
	}

	firstRead, err := os.ReadFile(filepath.Join(root, "mounted", "first.txt"))
	if err != nil {
		t.Fatalf("os.ReadFile(first.txt) error = %v", err)
	}
	if got := string(firstRead); got != "version one\n" {
		t.Fatalf("first.txt = %q, want %q", got, "version one\n")
	}
	secondRead, err := os.ReadFile(filepath.Join(root, "mounted", "second.txt"))
	if err != nil {
		t.Fatalf("os.ReadFile(second.txt) error = %v", err)
	}
	if got := string(secondRead); got != "version one\n" {
		t.Fatalf("second.txt = %q, want running worker to keep %q", got, "version one\n")
	}

	secondManager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
			return &scriptedDriver{
				decisions: []agent.Decision{
					withThought(shell("cat mounted/message.txt > mounted/third.txt"), "read mounted file after restart"),
					{Finish: &agent.FinishAction{Value: "third"}},
				},
			}, nil
		}),
		PollInterval: 50 * time.Millisecond,
	}

	runCtx, cancel = context.WithCancel(context.Background())
	errCh = make(chan error, 1)
	go func() {
		errCh <- secondManager.Run(runCtx)
	}()

	if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-mounted-refresh-third",
		RecipientAgentID: "worker",
		Payload:          "read third snapshot",
		MaxAttempts:      1,
	}); err != nil {
		t.Fatalf("EnqueueMessage(third) error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() (bool, error) {
		snapshot, err := s.SnapshotNamespace(ctx, namespace.ID)
		if err != nil {
			return false, err
		}
		return snapshot.Mailbox.Completed == 3, nil
	})

	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("second RuntimeManager.Run() error = %v, want %v", err, context.Canceled)
	}

	thirdRead, err := os.ReadFile(filepath.Join(root, "mounted", "third.txt"))
	if err != nil {
		t.Fatalf("os.ReadFile(third.txt) error = %v", err)
	}
	if got := string(thirdRead); got != "version two\n" {
		t.Fatalf("third.txt = %q, want %q after restart", got, "version two\n")
	}
}

func TestRuntimeManagerAddsHTTPHeaderGuidanceToSystemPrompt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-http-prompt")
	root := filepath.Join(t.TempDir(), "worker")
	value := "secret-http-header-value"
	if _, err := s.CreateAgent(ctx, cpstore.CreateAgentParams{
		NamespaceID:  namespace.ID,
		AgentID:      "worker",
		Name:         "worker",
		Role:         cpstore.AgentRoleWorker,
		DesiredState: cpstore.AgentDesiredStateRunning,
		RootPath:     root,
		ModelName:    "test-model",
		SystemPrompt: "You are a test worker.",
		MaxAttempts:  3,
		AllowNetwork: true,
		Config: managedAgentRuntimeConfig{
			Network: managedAgentNetworkConfig{
				ReachableHosts: []managedAgentHostMatcher{{Glob: "*"}},
			},
			HTTP: managedAgentHTTPConfig{
				Headers: []managedAgentHTTPHeader{{
					Name:  "Authorization",
					Value: &value,
					Domains: []managedAgentHTTPDomainMatcher{
						{Glob: "*.example.com"},
						{Regex: "^payments-[a-z0-9-]+\\.example\\.com$"},
					},
				}},
			},
		},
	}); err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}

	var (
		promptsMu sync.Mutex
		prompts   []string
	)
	manager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
			return agent.DriverFunc(func(_ context.Context, req agent.Request) (agent.Decision, error) {
				promptsMu.Lock()
				for _, message := range req.Messages {
					if message.Role == agent.MessageRoleSystem {
						prompts = append(prompts, message.Content)
					}
				}
				promptsMu.Unlock()
				return agent.Decision{Finish: &agent.FinishAction{Value: "ok"}}, nil
			}), nil
		}),
		PollInterval: 50 * time.Millisecond,
	}

	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- manager.Run(runCtx)
	}()

	if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-http-prompt",
		RecipientAgentID: "worker",
		Payload:          "start",
		MaxAttempts:      1,
	}); err != nil {
		t.Fatalf("EnqueueMessage() error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() (bool, error) {
		promptsMu.Lock()
		defer promptsMu.Unlock()
		return len(prompts) > 0, nil
	})

	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("RuntimeManager.Run() error = %v, want %v", err, context.Canceled)
	}

	promptsMu.Lock()
	combinedPrompts := strings.Join(prompts, "\n")
	promptsMu.Unlock()
	if !strings.Contains(combinedPrompts, "Automatic HTTP headers:") {
		t.Fatalf("combined system prompts = %q, want HTTP guidance header", combinedPrompts)
	}
	if !strings.Contains(combinedPrompts, "Authorization") {
		t.Fatalf("combined system prompts = %q, want header name guidance", combinedPrompts)
	}
	if !strings.Contains(combinedPrompts, "glob:*.example.com") {
		t.Fatalf("combined system prompts = %q, want glob domain guidance", combinedPrompts)
	}
	if !strings.Contains(combinedPrompts, "regex:^payments-[a-z0-9-]+\\.example\\.com$") {
		t.Fatalf("combined system prompts = %q, want regex domain guidance", combinedPrompts)
	}
	if strings.Contains(combinedPrompts, value) {
		t.Fatalf("combined system prompts = %q, want header value omitted", combinedPrompts)
	}
}

func TestWorkerFingerprintIncludesConfigJSON(t *testing.T) {
	t.Parallel()

	record := cpstore.RunnableAgent{
		AgentRecord: cpstore.AgentRecord{
			CurrentPromptVersionID: "prompt-1",
			RootPath:               "/tmp/worker",
			ModelProvider:          "openai",
			ModelName:              "gpt-5",
			UpdatedAt:              time.UnixMilli(1234),
			MaxSteps:               8,
			StepTimeout:            5 * time.Second,
			MaxOutputBytes:         1024,
			LeaseDuration:          time.Minute,
			ModelBaseURL:           "https://example.test",
			ConfigJSON:             `{"version":1,"type":"agent_config","body":{"mounts":[{"path":"mounted/file.txt","kind":"file","source":{"path":"../seed-one.txt","resolved_path":"/tmp/seed-one.txt"}}]}}`,
		},
		SystemPrompt: "test prompt",
	}
	updated := record
	updated.ConfigJSON = `{"version":1,"type":"agent_config","body":{"mounts":[{"path":"mounted/file.txt","kind":"file","source":{"path":"../seed-two.txt","resolved_path":"/tmp/seed-two.txt"}}]}}`

	if workerFingerprint(record) == workerFingerprint(updated) {
		t.Fatal("workerFingerprint() ignored ConfigJSON changes, want different fingerprints")
	}
}

func TestWorkerFingerprintIncludesReferencedEnvValues(t *testing.T) {
	t.Parallel()

	record := cpstore.RunnableAgent{
		AgentRecord: cpstore.AgentRecord{
			CurrentPromptVersionID: "prompt-1",
			RootPath:               "/tmp/worker",
			ModelProvider:          "openai",
			ModelName:              "gpt-5",
			UpdatedAt:              time.UnixMilli(1234),
			MaxSteps:               8,
			StepTimeout:            5 * time.Second,
			MaxOutputBytes:         1024,
			LeaseDuration:          time.Minute,
			ModelBaseURL:           "https://example.test",
			ConfigJSON:             `{"version":1,"type":"agent_config","body":{"mounts":[{"path":"mounted/file.txt","source":{"env_var":"EXAMPLE_API_KEY"}}],"http":{"headers":[{"name":"Authorization","env_var":"EXAMPLE_AUTHORIZATION"}]}}}`,
		},
		SystemPrompt: "test prompt",
	}

	first := workerFingerprintWithEnv(record, func(key string) string {
		switch key {
		case "EXAMPLE_API_KEY":
			return "one"
		case "EXAMPLE_AUTHORIZATION":
			return "alpha"
		default:
			return ""
		}
	})
	second := workerFingerprintWithEnv(record, func(key string) string {
		switch key {
		case "EXAMPLE_API_KEY":
			return "two"
		case "EXAMPLE_AUTHORIZATION":
			return "alpha"
		default:
			return ""
		}
	})

	if first == second {
		t.Fatal("workerFingerprintWithEnv() ignored referenced env changes, want different fingerprints")
	}
}
