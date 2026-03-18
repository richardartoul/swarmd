package server

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/richardartoul/swarmd/pkg/agent"
	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

func TestRuntimeManagerMaterializesMountedFilesInMemFS(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-memfs-mounted-files")
	createServerWorkerWithConfig(t, ctx, s, namespace.ID, "worker", "/workspace", nil, managedAgentRuntimeConfig{
		Filesystem: managedAgentFilesystemConfig{Kind: managedAgentFilesystemKindMemory},
		Mounts: []managedAgentMount{{
			Path:          "mounted/message.txt",
			ContentBase64: base64.StdEncoding.EncodeToString([]byte("mounted hello\n")),
		}},
	})

	manager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
			return &scriptedDriver{
				decisions: []agent.Decision{
					withThought(shell("cat mounted/message.txt"), "read mounted file"),
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
		ThreadID:         "thread-memfs-mounted-files",
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

	run, step := latestRunStepByPrompt(t, ctx, s, namespace.ID, "use mounts")
	if got := step.Stdout; got != "mounted hello\n" {
		t.Fatalf("step stdout for run %q = %q, want %q", run.ID, got, "mounted hello\n")
	}
}

func TestRuntimeManagerMemFSPersistsWarmStateAndResetsAfterRestart(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-memfs-persist")
	createServerWorkerWithConfig(t, ctx, s, namespace.ID, "worker", "/workspace", nil, managedAgentRuntimeConfig{
		Filesystem: managedAgentFilesystemConfig{Kind: managedAgentFilesystemKindMemory},
	})

	firstManager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
			return &scriptedDriver{
				decisions: []agent.Decision{
					withThought(shell("printf 'warm data\n' > note.txt"), "write warm state"),
					{Finish: &agent.FinishAction{Value: "seeded"}},
					withThought(shell("cat note.txt"), "read warm state"),
					{Finish: &agent.FinishAction{Value: "read"}},
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

	for idx, prompt := range []string{"seed memfs", "read memfs"} {
		if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
			NamespaceID:      namespace.ID,
			ThreadID:         "thread-warm-" + string(rune('a'+idx)),
			RecipientAgentID: "worker",
			Payload:          prompt,
			MaxAttempts:      1,
		}); err != nil {
			t.Fatalf("EnqueueMessage(%q) error = %v", prompt, err)
		}
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

	_, warmStep := latestRunStepByPrompt(t, ctx, s, namespace.ID, "read memfs")
	if got := warmStep.Stdout; got != "warm data\n" {
		t.Fatalf("warm read stdout = %q, want %q", got, "warm data\n")
	}

	secondManager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
			return &scriptedDriver{
				decisions: []agent.Decision{
					withThought(shell("if test -f note.txt; then echo stale; else echo reset; fi"), "check reset state"),
					{Finish: &agent.FinishAction{Value: "checked"}},
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
		ThreadID:         "thread-reset",
		RecipientAgentID: "worker",
		Payload:          "check reset",
		MaxAttempts:      1,
	}); err != nil {
		t.Fatalf("EnqueueMessage(reset) error = %v", err)
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

	_, resetStep := latestRunStepByPrompt(t, ctx, s, namespace.ID, "check reset")
	if got := resetStep.Stdout; got != "reset\n" {
		t.Fatalf("reset read stdout = %q, want %q", got, "reset\n")
	}
}

func latestRunStepByPrompt(t *testing.T, ctx context.Context, s *cpstore.Store, namespaceID, prompt string) (cpstore.RunRecord, cpstore.StepRecord) {
	t.Helper()

	runs, err := s.ListRuns(ctx, cpstore.ListRunsParams{
		NamespaceID: namespaceID,
		Limit:       20,
	})
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	for _, run := range runs {
		if run.TriggerPrompt != prompt {
			continue
		}
		steps, err := s.ListStepsByRun(ctx, namespaceID, run.ID)
		if err != nil {
			t.Fatalf("ListStepsByRun() error = %v", err)
		}
		if len(steps) == 0 {
			t.Fatalf("run %q had no steps", run.ID)
		}
		return run, steps[len(steps)-1]
	}
	t.Fatalf("no run found for prompt %q", prompt)
	return cpstore.RunRecord{}, cpstore.StepRecord{}
}
