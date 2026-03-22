package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

func TestSyncSpecsFromConfigRootCreatesUpdatesAndDeletesAgents(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newServerStore(t)
	rootBase := filepath.Join(t.TempDir(), "roots")
	configRoot := t.TempDir()

	namespace := createServerNamespace(t, ctx, store, "default")
	createServerWorker(t, ctx, store, namespace.ID, "stale-agent", filepath.Join(rootBase, namespace.ID, "stale-agent"))

	writeAgentSpec(t, configRoot, "default", "google-curl-check", `
version: 1
name: Google Curl Check
model:
  name: gpt-5
prompt: |
  Run curl https://www.google.com and report whether it worked.
network:
  reachable_hosts:
    - glob: "*"
tools:
  - server_log
runtime:
  max_steps: 4
  step_timeout: 45s
schedules:
  - cron: "* * * * *"
`)

	firstSummary, err := SyncSpecsFromConfigRoot(ctx, store, configRoot, rootBase)
	if err != nil {
		t.Fatalf("SyncSpecsFromConfigRoot() first error = %v", err)
	}
	if firstSummary.AgentsCreated != 1 {
		t.Fatalf("firstSummary.AgentsCreated = %d, want 1", firstSummary.AgentsCreated)
	}
	if firstSummary.AgentsDeleted != 1 {
		t.Fatalf("firstSummary.AgentsDeleted = %d, want 1", firstSummary.AgentsDeleted)
	}
	if firstSummary.SchedulesCreated != 1 {
		t.Fatalf("firstSummary.SchedulesCreated = %d, want 1", firstSummary.SchedulesCreated)
	}

	agentRecord, err := store.GetAgent(ctx, "default", "google-curl-check")
	if err != nil {
		t.Fatalf("GetAgent() error = %v", err)
	}
	network, err := loadAgentNetworkSettings(agentRecord.ConfigJSON)
	if err != nil {
		t.Fatalf("loadAgentNetworkSettings() error = %v", err)
	}
	if len(network.ReachableHosts) != 1 || network.ReachableHosts[0].Glob != "*" {
		t.Fatalf("loadAgentNetworkSettings() = %#v, want reachable_hosts glob *", network)
	}
	if !GlobalNetworkEnabledFromConfigJSON(agentRecord.ConfigJSON) {
		t.Fatal("GlobalNetworkEnabledFromConfigJSON(agentRecord.ConfigJSON) = false, want true")
	}
	tools, err := loadAgentToolSettings(agentRecord.ConfigJSON)
	if err != nil {
		t.Fatalf("loadAgentToolSettings() error = %v", err)
	}
	if len(tools) != 1 || tools[0].ID != "server_log" {
		t.Fatalf("loadAgentToolSettings() = %#v, want [server_log]", tools)
	}
	expectedRoot := filepath.Join(rootBase, "default", "google-curl-check")
	if agentRecord.RootPath != expectedRoot {
		t.Fatalf("agentRecord.RootPath = %q, want %q", agentRecord.RootPath, expectedRoot)
	}
	info, err := os.Stat(agentRecord.RootPath)
	if err != nil {
		t.Fatalf("os.Stat(%q) error = %v", agentRecord.RootPath, err)
	}
	if !info.IsDir() {
		t.Fatalf("agent root %q is not a directory", agentRecord.RootPath)
	}
	if agentRecord.StepTimeout != 45*time.Second {
		t.Fatalf("agentRecord.StepTimeout = %s, want %s", agentRecord.StepTimeout, 45*time.Second)
	}
	if agentRecord.SystemPrompt == "" {
		t.Fatal("agentRecord.SystemPrompt was empty")
	}
	if _, err := store.GetAgent(ctx, "default", "stale-agent"); !errors.Is(err, cpstore.ErrNotFound) {
		t.Fatalf("GetAgent() for stale-agent error = %v, want ErrNotFound", err)
	}

	writeAgentSpec(t, configRoot, "default", "google-curl-check", `
version: 1
name: Google Curl Check
model:
  name: gpt-5
prompt: |
  Run curl https://www.google.com and capture the status code.
network:
  reachable_hosts:
    - glob: "*"
tools:
  - server_log
runtime:
  max_steps: 6
  step_timeout: 1m
schedules:
  - cron: "*/5 * * * *"
`)

	secondSummary, err := SyncSpecsFromConfigRoot(ctx, store, configRoot, rootBase)
	if err != nil {
		t.Fatalf("SyncSpecsFromConfigRoot() second error = %v", err)
	}
	if secondSummary.AgentsUpdated != 1 {
		t.Fatalf("secondSummary.AgentsUpdated = %d, want 1", secondSummary.AgentsUpdated)
	}
	if secondSummary.SchedulesDeleted != 1 {
		t.Fatalf("secondSummary.SchedulesDeleted = %d, want 1", secondSummary.SchedulesDeleted)
	}
	if secondSummary.SchedulesCreated != 1 {
		t.Fatalf("secondSummary.SchedulesCreated = %d, want 1", secondSummary.SchedulesCreated)
	}

	updatedAgent, err := store.GetAgent(ctx, "default", "google-curl-check")
	if err != nil {
		t.Fatalf("GetAgent() updated error = %v", err)
	}
	if updatedAgent.StepTimeout != time.Minute {
		t.Fatalf("updatedAgent.StepTimeout = %s, want %s", updatedAgent.StepTimeout, time.Minute)
	}
	if updatedAgent.MaxSteps != 6 {
		t.Fatalf("updatedAgent.MaxSteps = %d, want 6", updatedAgent.MaxSteps)
	}
	if updatedAgent.SystemPrompt == agentRecord.SystemPrompt {
		t.Fatal("system prompt did not update")
	}

	if err := os.Remove(filepath.Join(configRoot, "namespaces", "default", "agents", "google-curl-check.yaml")); err != nil {
		t.Fatalf("os.Remove() error = %v", err)
	}
	thirdSummary, err := SyncSpecsFromConfigRoot(ctx, store, configRoot, rootBase)
	if err != nil {
		t.Fatalf("SyncSpecsFromConfigRoot() third error = %v", err)
	}
	if thirdSummary.AgentsDeleted != 1 {
		t.Fatalf("thirdSummary.AgentsDeleted = %d, want 1", thirdSummary.AgentsDeleted)
	}
	if _, err := store.GetAgent(ctx, "default", "google-curl-check"); !errors.Is(err, cpstore.ErrNotFound) {
		t.Fatalf("GetAgent() after delete error = %v, want ErrNotFound", err)
	}
}

func TestSyncSpecsFromConfigRootRejectsDuplicateAgentRoots(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newServerStore(t)
	configRoot := t.TempDir()
	rootBase := filepath.Join(t.TempDir(), "roots")

	writeAgentSpec(t, configRoot, "default", "worker-a", `
version: 1
model:
  name: gpt-5
prompt: hello
root_path: shared-root
`)
	writeAgentSpec(t, configRoot, "default", "worker-b", `
version: 1
model:
  name: gpt-5
prompt: hello
root_path: shared-root
`)

	_, err := SyncSpecsFromConfigRoot(ctx, store, configRoot, rootBase)
	if err == nil {
		t.Fatal("SyncSpecsFromConfigRoot() error = nil, want duplicate root path error")
	}
	if !strings.Contains(err.Error(), "same root path") {
		t.Fatalf("SyncSpecsFromConfigRoot() error = %v, want duplicate root path error", err)
	}
}

func TestSyncSpecsFromConfigRootSupportsMemoryFilesystem(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newServerStore(t)
	configRoot := t.TempDir()
	rootBase := filepath.Join(t.TempDir(), "roots")

	writeAgentSpec(t, configRoot, "default", "worker-a", `
version: 1
model:
  name: gpt-5
prompt: hello
runtime:
  filesystem:
    kind: memory
`)
	writeAgentSpec(t, configRoot, "default", "worker-b", `
version: 1
model:
  name: gpt-5
prompt: hello
runtime:
  filesystem:
    kind: memory
`)

	summary, err := SyncSpecsFromConfigRoot(ctx, store, configRoot, rootBase)
	if err != nil {
		t.Fatalf("SyncSpecsFromConfigRoot() error = %v", err)
	}
	if summary.AgentsCreated != 2 {
		t.Fatalf("summary.AgentsCreated = %d, want 2", summary.AgentsCreated)
	}

	for _, agentID := range []string{"worker-a", "worker-b"} {
		record, err := store.GetAgent(ctx, "default", agentID)
		if err != nil {
			t.Fatalf("GetAgent(%q) error = %v", agentID, err)
		}
		if got := record.RootPath; got != string(os.PathSeparator) {
			t.Fatalf("record.RootPath = %q, want %q", got, string(os.PathSeparator))
		}
		filesystem, err := loadAgentFilesystemSettings(record.ConfigJSON)
		if err != nil {
			t.Fatalf("loadAgentFilesystemSettings() error = %v", err)
		}
		if got := filesystem.kind(); got != managedAgentFilesystemKindMemory {
			t.Fatalf("filesystem.kind = %q, want %q", got, managedAgentFilesystemKindMemory)
		}
		if _, err := os.Stat(filepath.Join(rootBase, "default", agentID)); !os.IsNotExist(err) {
			t.Fatalf("os.Stat(memory root dir) error = %v, want not exists", err)
		}
	}
}

func TestSyncSpecsFromConfigRootPersistsMemorySettings(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newServerStore(t)
	configRoot := t.TempDir()
	rootBase := filepath.Join(t.TempDir(), "roots")

	writeAgentSpec(t, configRoot, "default", "memory-worker", `
version: 1
model:
  name: gpt-5
prompt: hello
memory:
  prompt_override: |
    Read .memory/ROOT.md first.
`)

	if _, err := SyncSpecsFromConfigRoot(ctx, store, configRoot, rootBase); err != nil {
		t.Fatalf("SyncSpecsFromConfigRoot() first error = %v", err)
	}

	record, err := store.GetAgent(ctx, "default", "memory-worker")
	if err != nil {
		t.Fatalf("GetAgent() first error = %v", err)
	}
	memory := decodeManagedAgentMemory(t, record.ConfigJSON)
	if got := strings.TrimSpace(memory.PromptOverride); got != "Read .memory/ROOT.md first." {
		t.Fatalf("memory.PromptOverride = %q, want persisted prompt override", got)
	}
	if memory.Disable {
		t.Fatal("memory.Disable = true, want false")
	}

	writeAgentSpec(t, configRoot, "default", "memory-worker", `
version: 1
model:
  name: gpt-5
prompt: hello again
memory:
  disable: true
`)

	if _, err := SyncSpecsFromConfigRoot(ctx, store, configRoot, rootBase); err != nil {
		t.Fatalf("SyncSpecsFromConfigRoot() second error = %v", err)
	}

	record, err = store.GetAgent(ctx, "default", "memory-worker")
	if err != nil {
		t.Fatalf("GetAgent() second error = %v", err)
	}
	memory = decodeManagedAgentMemory(t, record.ConfigJSON)
	if !memory.Disable {
		t.Fatal("memory.Disable = false, want true")
	}
	if got := strings.TrimSpace(memory.PromptOverride); got != "" {
		t.Fatalf("memory.PromptOverride = %q, want empty after disable update", got)
	}
}

func TestSyncSpecsFromConfigRootPersistsToolSettings(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newServerStore(t)
	configRoot := t.TempDir()
	rootBase := filepath.Join(t.TempDir(), "roots")
	registerSpecsTestCustomTools()

	writeAgentSpec(t, configRoot, "default", "tools-worker", `
version: 1
model:
  name: gpt-5
prompt: hello
tools:
  - `+specsTestCustomToolOne+`
  - id: `+specsTestCustomToolTwo+`
    config:
      owner: qa
`)

	if _, err := SyncSpecsFromConfigRoot(ctx, store, configRoot, rootBase); err != nil {
		t.Fatalf("SyncSpecsFromConfigRoot() error = %v", err)
	}

	record, err := store.GetAgent(ctx, "default", "tools-worker")
	if err != nil {
		t.Fatalf("GetAgent() error = %v", err)
	}
	tools, err := loadAgentToolSettings(record.ConfigJSON)
	if err != nil {
		t.Fatalf("loadAgentToolSettings() error = %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("len(tools) = %d, want 2", len(tools))
	}
	if tools[0].ID != specsTestCustomToolOne || tools[1].ID != specsTestCustomToolTwo {
		t.Fatalf("tools = %#v, want [%s %s]", tools, specsTestCustomToolOne, specsTestCustomToolTwo)
	}
	if got := tools[1].Config["owner"]; got != "qa" {
		t.Fatalf("tools[1].Config[owner] = %#v, want %q", got, "qa")
	}
}

func TestSyncSpecsFromConfigRootPersistsCompactPathMountMetadata(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newServerStore(t)
	configRoot := t.TempDir()
	rootBase := filepath.Join(t.TempDir(), "roots")
	sourcePath := filepath.Join(configRoot, "namespaces", "default", "seed.txt")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", filepath.Dir(sourcePath), err)
	}
	if err := os.WriteFile(sourcePath, []byte("version one"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", sourcePath, err)
	}
	resolvedSourcePath, err := filepath.EvalSymlinks(sourcePath)
	if err != nil {
		t.Fatalf("filepath.EvalSymlinks(%q) error = %v", sourcePath, err)
	}
	templateDir := filepath.Join(configRoot, "namespaces", "default", "templates")
	if err := os.MkdirAll(filepath.Join(templateDir, "partials"), 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", filepath.Join(templateDir, "partials"), err)
	}
	if err := os.WriteFile(filepath.Join(templateDir, "summary.md"), []byte("summary template"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(summary.md) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(templateDir, "partials", "header.txt"), []byte("header template"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(header.txt) error = %v", err)
	}
	resolvedTemplateDir, err := filepath.EvalSymlinks(templateDir)
	if err != nil {
		t.Fatalf("filepath.EvalSymlinks(%q) error = %v", templateDir, err)
	}

	writeAgentSpec(t, configRoot, "default", "mount-worker", `
version: 1
model:
  name: gpt-5
prompt: hello
mounts:
  - path: mounted/source.txt
    description: Host-backed single file.
    source:
      path: ../seed.txt
  - path: mounted/inline.txt
    description: Inline example file.
    source:
      inline: inline mount
  - path: mounted/templates
    description: Reusable templates directory.
    source:
      path: ../templates
`)

	if _, err := SyncSpecsFromConfigRoot(ctx, store, configRoot, rootBase); err != nil {
		t.Fatalf("SyncSpecsFromConfigRoot() first error = %v", err)
	}

	record, err := store.GetAgent(ctx, "default", "mount-worker")
	if err != nil {
		t.Fatalf("GetAgent() first error = %v", err)
	}
	firstConfigJSON := record.ConfigJSON
	if strings.Contains(firstConfigJSON, "version one") {
		t.Fatalf("first ConfigJSON unexpectedly embedded file contents: %q", firstConfigJSON)
	}
	if strings.Contains(firstConfigJSON, "summary template") {
		t.Fatalf("first ConfigJSON unexpectedly embedded directory contents: %q", firstConfigJSON)
	}
	mounts, err := loadAgentMountSettings(record.ConfigJSON)
	if err != nil {
		t.Fatalf("loadAgentMountSettings() first error = %v", err)
	}
	if len(mounts) != 3 {
		t.Fatalf("len(mounts) = %d, want 3", len(mounts))
	}
	fileMount := findManagedMount(t, mounts, "mounted/source.txt")
	if got := fileMount.Description; got != "Host-backed single file." {
		t.Fatalf("fileMount.Description = %q, want file description", got)
	}
	if got := fileMount.Source.Path; got != "../seed.txt" {
		t.Fatalf("fileMount.Source.Path = %q, want %q", got, "../seed.txt")
	}
	if got := fileMount.Source.ResolvedPath; got != resolvedSourcePath {
		t.Fatalf("fileMount.Source.ResolvedPath = %q, want %q", got, resolvedSourcePath)
	}
	if got := fileMount.kind(); got != managedAgentMountKindFile {
		t.Fatalf("fileMount.kind() = %q, want %q", got, managedAgentMountKindFile)
	}
	if got := fileMount.ContentBase64; got != "" {
		t.Fatalf("fileMount.ContentBase64 = %q, want empty for deferred path mount", got)
	}
	inlineMount := findManagedMount(t, mounts, "mounted/inline.txt")
	if got := inlineMount.Description; got != "Inline example file." {
		t.Fatalf("inlineMount.Description = %q, want inline description", got)
	}
	if got := string(mustManagedMountContent(t, inlineMount)); got != "inline mount" {
		t.Fatalf("inlineMount content = %q, want %q", got, "inline mount")
	}
	dirMount := findManagedMount(t, mounts, "mounted/templates")
	if got := dirMount.Description; got != "Reusable templates directory." {
		t.Fatalf("dirMount.Description = %q, want directory description", got)
	}
	if got := dirMount.kind(); got != managedAgentMountKindDirectory {
		t.Fatalf("dirMount.kind() = %q, want %q", got, managedAgentMountKindDirectory)
	}
	if got := dirMount.Source.Path; got != "../templates" {
		t.Fatalf("dirMount.Source.Path = %q, want %q", got, "../templates")
	}
	if got := dirMount.Source.ResolvedPath; got != resolvedTemplateDir {
		t.Fatalf("dirMount.Source.ResolvedPath = %q, want %q", got, resolvedTemplateDir)
	}
	if got := dirMount.ContentBase64; got != "" {
		t.Fatalf("dirMount.ContentBase64 = %q, want empty for deferred path mount", got)
	}

	if err := os.WriteFile(sourcePath, []byte("version two"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) update error = %v", sourcePath, err)
	}
	if _, err := SyncSpecsFromConfigRoot(ctx, store, configRoot, rootBase); err != nil {
		t.Fatalf("SyncSpecsFromConfigRoot() second error = %v", err)
	}

	record, err = store.GetAgent(ctx, "default", "mount-worker")
	if err != nil {
		t.Fatalf("GetAgent() second error = %v", err)
	}
	if got := record.ConfigJSON; got != firstConfigJSON {
		t.Fatalf("ConfigJSON changed after only source content changed\nold: %s\nnew: %s", firstConfigJSON, got)
	}
	mounts, err = loadAgentMountSettings(record.ConfigJSON)
	if err != nil {
		t.Fatalf("loadAgentMountSettings() second error = %v", err)
	}
	fileMount = findManagedMount(t, mounts, "mounted/source.txt")
	if got := fileMount.Source.ResolvedPath; got != resolvedSourcePath {
		t.Fatalf("fileMount.Source.ResolvedPath = %q, want %q after resync", got, resolvedSourcePath)
	}
}

func TestSyncSpecsFromConfigRootUpdatesConfigWhenMountMetadataChanges(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newServerStore(t)
	configRoot := t.TempDir()
	rootBase := filepath.Join(t.TempDir(), "roots")
	firstSourcePath := filepath.Join(configRoot, "namespaces", "default", "seed-one.txt")
	secondSourcePath := filepath.Join(configRoot, "namespaces", "default", "seed-two.txt")
	if err := os.MkdirAll(filepath.Dir(firstSourcePath), 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", filepath.Dir(firstSourcePath), err)
	}
	if err := os.WriteFile(firstSourcePath, []byte("version one"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", firstSourcePath, err)
	}
	if err := os.WriteFile(secondSourcePath, []byte("version two"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", secondSourcePath, err)
	}
	resolvedSecondSourcePath, err := filepath.EvalSymlinks(secondSourcePath)
	if err != nil {
		t.Fatalf("filepath.EvalSymlinks(%q) error = %v", secondSourcePath, err)
	}

	writeAgentSpec(t, configRoot, "default", "mount-worker", `
version: 1
model:
  name: gpt-5
prompt: hello
mounts:
  - path: mounted/source.txt
    source:
      path: ../seed-one.txt
`)

	if _, err := SyncSpecsFromConfigRoot(ctx, store, configRoot, rootBase); err != nil {
		t.Fatalf("SyncSpecsFromConfigRoot() first error = %v", err)
	}

	record, err := store.GetAgent(ctx, "default", "mount-worker")
	if err != nil {
		t.Fatalf("GetAgent() first error = %v", err)
	}
	firstConfigJSON := record.ConfigJSON

	writeAgentSpec(t, configRoot, "default", "mount-worker", `
version: 1
model:
  name: gpt-5
prompt: hello
mounts:
  - path: mounted/source.txt
    source:
      path: ../seed-two.txt
`)

	if _, err := SyncSpecsFromConfigRoot(ctx, store, configRoot, rootBase); err != nil {
		t.Fatalf("SyncSpecsFromConfigRoot() second error = %v", err)
	}

	record, err = store.GetAgent(ctx, "default", "mount-worker")
	if err != nil {
		t.Fatalf("GetAgent() second error = %v", err)
	}
	if got := record.ConfigJSON; got == firstConfigJSON {
		t.Fatalf("ConfigJSON did not change after mount metadata changed: %s", got)
	}
	mounts, err := loadAgentMountSettings(record.ConfigJSON)
	if err != nil {
		t.Fatalf("loadAgentMountSettings() error = %v", err)
	}
	fileMount := findManagedMount(t, mounts, "mounted/source.txt")
	if got := fileMount.Source.Path; got != "../seed-two.txt" {
		t.Fatalf("fileMount.Source.Path = %q, want %q", got, "../seed-two.txt")
	}
	if got := fileMount.Source.ResolvedPath; got != resolvedSecondSourcePath {
		t.Fatalf("fileMount.Source.ResolvedPath = %q, want %q", got, resolvedSecondSourcePath)
	}
}
