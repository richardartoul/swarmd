package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

func TestRunConfigValidatePrintsSummary(t *testing.T) {
	t.Parallel()

	configRoot := filepath.Join(t.TempDir(), "config")
	writeCommandSpec(t, filepath.Join(configRoot, "namespaces", "default", "agents", "worker-a.yaml"), `
version: 1
model:
  name: test-model
prompt: hello
schedules:
  - cron: "* * * * *"
`)

	var stdout bytes.Buffer
	if err := run(context.Background(), []string{"config", "validate", "-config-root", configRoot}, commandIO{
		stdout: &stdout,
		stderr: &stdout,
	}); err != nil {
		t.Fatalf("run(config validate) error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "config> valid") {
		t.Fatalf("config validate output = %q, want valid summary", output)
	}
	if !strings.Contains(output, "namespaces=1 agents=1 schedules=1") {
		t.Fatalf("config validate output = %q, want namespace/agent/schedule counts", output)
	}
}

func TestRunConfigValidateRejectsMissingToolEnv(t *testing.T) {
	configRoot := filepath.Join(t.TempDir(), "config")
	writeCommandSpec(t, filepath.Join(configRoot, "namespaces", "default", "agents", "worker-a.yaml"), `
version: 1
model:
  name: test-model
prompt: hello
network:
  reachable_hosts:
    - glob: "*"
tools:
  - slack_post
`)
	t.Setenv("SLACK_USER_TOKEN", "")

	var stdout bytes.Buffer
	err := run(context.Background(), []string{"config", "validate", "-config-root", configRoot}, commandIO{
		stdout: &stdout,
		stderr: &stdout,
	})
	if err == nil {
		t.Fatal("run(config validate) error = nil, want missing tool env failure")
	}
	if !strings.Contains(err.Error(), "SLACK_USER_TOKEN") {
		t.Fatalf("run(config validate) error = %v, want missing SLACK_USER_TOKEN", err)
	}
}

func TestRunConfigValidateRejectsMissingSlackHistoryToolEnv(t *testing.T) {
	configRoot := filepath.Join(t.TempDir(), "config")
	writeCommandSpec(t, filepath.Join(configRoot, "namespaces", "default", "agents", "worker-a.yaml"), `
version: 1
model:
  name: test-model
prompt: hello
network:
  reachable_hosts:
    - glob: "*"
tools:
  - slack_channel_history
`)
	t.Setenv("SLACK_USER_TOKEN", "")

	var stdout bytes.Buffer
	err := run(context.Background(), []string{"config", "validate", "-config-root", configRoot}, commandIO{
		stdout: &stdout,
		stderr: &stdout,
	})
	if err == nil {
		t.Fatal("run(config validate) error = nil, want missing tool env failure")
	}
	if !strings.Contains(err.Error(), "SLACK_USER_TOKEN") {
		t.Fatalf("run(config validate) error = %v, want missing SLACK_USER_TOKEN", err)
	}
}

func TestRunConfigValidateRejectsMissingSlackDMToolEnv(t *testing.T) {
	configRoot := filepath.Join(t.TempDir(), "config")
	writeCommandSpec(t, filepath.Join(configRoot, "namespaces", "default", "agents", "worker-a.yaml"), `
version: 1
model:
  name: test-model
prompt: hello
network:
  reachable_hosts:
    - glob: "*"
tools:
  - slack_dm
`)
	t.Setenv("SLACK_USER_TOKEN", "")

	var stdout bytes.Buffer
	err := run(context.Background(), []string{"config", "validate", "-config-root", configRoot}, commandIO{
		stdout: &stdout,
		stderr: &stdout,
	})
	if err == nil {
		t.Fatal("run(config validate) error = nil, want missing tool env failure")
	}
	if !strings.Contains(err.Error(), "SLACK_USER_TOKEN") {
		t.Fatalf("run(config validate) error = %v, want missing SLACK_USER_TOKEN", err)
	}
}

func TestRunSyncPlanDoesNotMutateState(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "server.db")
	configRoot := filepath.Join(tempDir, "config")
	rootBase := filepath.Join(tempDir, "roots")
	writeCommandSpec(t, filepath.Join(configRoot, "namespaces", "default", "agents", "planned-worker.yaml"), `
version: 1
model:
  name: test-model
prompt: planned prompt
`)

	store := openCommandTestStore(t, dbPath)
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	var stdout bytes.Buffer
	if err := run(ctx, []string{
		"sync", "plan",
		"-db", dbPath,
		"-config-root", configRoot,
		"-root-base", rootBase,
	}, commandIO{
		stdout: &stdout,
		stderr: &stdout,
	}); err != nil {
		t.Fatalf("run(sync plan) error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "agent create default/planned-worker") {
		t.Fatalf("sync plan output = %q, want planned agent create", output)
	}

	store = openCommandTestStore(t, dbPath)
	defer store.Close()
	agents, err := store.ListAgents(ctx, cpstore.ListAgentsParams{})
	if err != nil {
		t.Fatalf("ListAgents() error = %v", err)
	}
	if len(agents) != 0 {
		t.Fatalf("len(ListAgents()) = %d, want 0 because sync plan must be read-only", len(agents))
	}
}

func TestReadOnlyCommandsFallbackToLocalDBWhenDefaultMissing(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	t.Setenv("SWARMD_SERVER_DB", "")
	chdirForTest(t, tempDir)

	dbPath := filepath.Join(tempDir, "local.db")
	store := openCommandTestStore(t, dbPath)
	namespace, err := store.CreateNamespace(ctx, cpstore.CreateNamespaceParams{
		ID:   "local-fallback",
		Name: "local fallback",
	})
	if err != nil {
		t.Fatalf("CreateNamespace() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	var stdout bytes.Buffer
	if err := run(ctx, []string{"namespaces", "ls"}, commandIO{
		stdout: &stdout,
		stderr: &stdout,
	}); err != nil {
		t.Fatalf("run(namespaces ls) error = %v", err)
	}
	if output := stdout.String(); !strings.Contains(output, namespace.ID) {
		t.Fatalf("namespaces ls output = %q, want namespace %q from local fallback db", output, namespace.ID)
	}
}

func TestReadOnlyCommandsReportMissingDefaultDatabase(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	t.Setenv("SWARMD_SERVER_DB", "")
	chdirForTest(t, tempDir)

	var stdout bytes.Buffer
	err := run(ctx, []string{"namespaces", "ls"}, commandIO{
		stdout: &stdout,
		stderr: &stdout,
	})
	if err == nil {
		t.Fatal("run(namespaces ls) error = nil, want missing database failure")
	}
	wantPath := filepath.Join(tempDir, defaultDataDir, defaultSQLiteFilename)
	if !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("run(namespaces ls) error = %v, want missing database message", err)
	}
	if !strings.Contains(err.Error(), wantPath) {
		t.Fatalf("run(namespaces ls) error = %v, want missing path %q", err, wantPath)
	}
}

func TestReadOnlyCommandsRespectExplicitMissingDBPath(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	t.Setenv("SWARMD_SERVER_DB", "")
	chdirForTest(t, tempDir)

	store := openCommandTestStore(t, filepath.Join(tempDir, "local.db"))
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	var stdout bytes.Buffer
	err := run(ctx, []string{"namespaces", "ls", "-db", "./missing.db"}, commandIO{
		stdout: &stdout,
		stderr: &stdout,
	})
	if err == nil {
		t.Fatal("run(namespaces ls -db ./missing.db) error = nil, want missing database failure")
	}
	wantPath := filepath.Join(tempDir, "missing.db")
	if !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("run(namespaces ls -db ./missing.db) error = %v, want missing database message", err)
	}
	if !strings.Contains(err.Error(), wantPath) {
		t.Fatalf("run(namespaces ls -db ./missing.db) error = %v, want missing path %q", err, wantPath)
	}
}

func TestRunShowOutputsSteps(t *testing.T) {
	t.Parallel()

	fixture := seedCommandRunFixture(t)

	var stdout bytes.Buffer
	if err := run(context.Background(), []string{
		"run", "show",
		"-db", fixture.dbPath,
		"-namespace", fixture.namespaceID,
		"-run", fixture.runID,
	}, commandIO{
		stdout: &stdout,
		stderr: &stdout,
	}); err != nil {
		t.Fatalf("run(run show) error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "step 1") {
		t.Fatalf("run show output = %q, want step section", output)
	}
	if !strings.Contains(output, "printf 'hi'") {
		t.Fatalf("run show output = %q, want shell command", output)
	}
	if !strings.Contains(output, "stdout:") || !strings.Contains(output, "hi") {
		t.Fatalf("run show output = %q, want stdout block", output)
	}
	if !strings.Contains(output, "trigger_prompt:\n  {\n    \"text\": \"hello\"\n  }\n") {
		t.Fatalf("run show output = %q, want trigger prompt block", output)
	}
	if !strings.Contains(output, "system_prompt:\n  System prompt snapshot\n") {
		t.Fatalf("run show output = %q, want system prompt block", output)
	}
}

func TestMailboxShowOutputsDecodedPayload(t *testing.T) {
	t.Parallel()

	fixture := seedCommandRunFixture(t)

	var stdout bytes.Buffer
	if err := run(context.Background(), []string{
		"mailbox", "show",
		"-db", fixture.dbPath,
		"-namespace", fixture.namespaceID,
		"-message", fixture.messageID,
	}, commandIO{
		stdout: &stdout,
		stderr: &stdout,
	}); err != nil {
		t.Fatalf("run(mailbox show) error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "Mailbox Message") {
		t.Fatalf("mailbox show output = %q, want header", output)
	}
	if !strings.Contains(output, "\"text\": \"hello\"") {
		t.Fatalf("mailbox show output = %q, want decoded payload", output)
	}
	if !strings.Contains(output, "\"source\": \"test\"") {
		t.Fatalf("mailbox show output = %q, want decoded metadata", output)
	}
}

type commandRunFixture struct {
	dbPath       string
	namespaceID  string
	agentID      string
	messageID    string
	runID        string
	systemPrompt string
}

func seedCommandRunFixture(t *testing.T) commandRunFixture {
	t.Helper()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "server.db")
	store := openCommandTestStore(t, dbPath)
	defer store.Close()

	namespace, err := store.CreateNamespace(ctx, cpstore.CreateNamespaceParams{Name: "command-namespace"})
	if err != nil {
		t.Fatalf("CreateNamespace() error = %v", err)
	}
	agentRecord, err := store.CreateAgent(ctx, cpstore.CreateAgentParams{
		NamespaceID:  namespace.ID,
		AgentID:      "command-agent",
		Name:         "command-agent",
		Role:         cpstore.AgentRoleWorker,
		DesiredState: cpstore.AgentDesiredStateRunning,
		RootPath:     filepath.Join(t.TempDir(), "command-agent"),
		ModelName:    "test-model",
		SystemPrompt: "You are a test worker.",
		MaxAttempts:  3,
	})
	if err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}

	message, err := store.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-command",
		RecipientAgentID: agentRecord.ID,
		Kind:             "user.prompt",
		Payload:          map[string]any{"text": "hello"},
		Metadata:         map[string]any{"source": "test"},
		MaxAttempts:      3,
	})
	if err != nil {
		t.Fatalf("EnqueueMessage() error = %v", err)
	}

	const systemPrompt = "System prompt snapshot"
	claimed, err := store.ClaimNextMessage(ctx, cpstore.ClaimMessageParams{
		NamespaceID:   namespace.ID,
		AgentID:       agentRecord.ID,
		LeaseDuration: time.Minute,
		SystemPrompt:  systemPrompt,
	})
	if err != nil {
		t.Fatalf("ClaimNextMessage() error = %v", err)
	}

	startedAt := time.Now().UTC()
	finishedAt := startedAt.Add(20 * time.Millisecond)
	if err := store.RecordStep(ctx, cpstore.StepRecord{
		NamespaceID:       namespace.ID,
		RunID:             claimed.Run.ID,
		MessageID:         claimed.Message.ID,
		AgentID:           agentRecord.ID,
		StepIndex:         1,
		Thought:           "print hi",
		Shell:             "printf 'hi'",
		UsageCachedTokens: 2,
		CWDBefore:         agentRecord.RootPath,
		CWDAfter:          agentRecord.RootPath,
		Stdout:            "hi",
		StartedAt:         startedAt,
		FinishedAt:        finishedAt,
		Duration:          finishedAt.Sub(startedAt),
		Status:            "ok",
	}); err != nil {
		t.Fatalf("RecordStep() error = %v", err)
	}
	if err := store.CompleteRun(ctx, cpstore.CompleteRunParams{
		NamespaceID:       namespace.ID,
		RunID:             claimed.Run.ID,
		MessageID:         claimed.Message.ID,
		Status:            "finished",
		FinishedAt:        finishedAt,
		Duration:          finishedAt.Sub(startedAt),
		CWD:               agentRecord.RootPath,
		UsageCachedTokens: 2,
		Value:             map[string]any{"ok": true},
	}); err != nil {
		t.Fatalf("CompleteRun() error = %v", err)
	}

	return commandRunFixture{
		dbPath:       dbPath,
		namespaceID:  namespace.ID,
		agentID:      agentRecord.ID,
		messageID:    message.ID,
		runID:        claimed.Run.ID,
		systemPrompt: systemPrompt,
	}
}

func openCommandTestStore(t *testing.T, dbPath string) *cpstore.Store {
	t.Helper()
	store, err := cpstore.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("cpstore.Open() error = %v", err)
	}
	return store
}

func writeCommandSpec(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}

func chdirForTest(t *testing.T, dir string) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd() error = %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("os.Chdir(%q) error = %v", dir, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatalf("restore cwd %q: %v", cwd, err)
		}
	})
}
