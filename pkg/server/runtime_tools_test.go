package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/richardartoul/swarmd/pkg/agent"
	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

func TestRuntimeManagerServerLogToolWritesToRuntimeLogs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-server-log")
	createServerWorkerWithCommands(t, ctx, s, namespace.ID, "worker", filepath.Join(t.TempDir(), "worker"), []string{"server_log"})

	var logStdout, logStderr bytes.Buffer
	manager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
			return &scriptedDriver{
				decisions: []agent.Decision{
					{
						Thought: "write server log",
						Tool: &agent.ToolAction{
							Name:  "server_log",
							Kind:  agent.ToolKindFunction,
							Input: `{"level":"info","message":"hello from worker"}`,
						},
					},
					{Finish: &agent.FinishAction{Value: "ok"}},
				},
			}, nil
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
		ThreadID:         "thread-server-log",
		RecipientAgentID: "worker",
		Payload:          "start",
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
	if got := logStdout.String(); !strings.Contains(got, "agent_log> "+namespace.ID+"/worker") {
		t.Fatalf("log stdout = %q, want agent log line", got)
	}
	if got := logStdout.String(); !strings.Contains(got, `level=info`) {
		t.Fatalf("log stdout = %q, want server_log level", got)
	}
	if got := logStdout.String(); !strings.Contains(got, `hello from worker`) {
		t.Fatalf("log stdout = %q, want server_log message", got)
	}
}

func TestRuntimeManagerIncludesManagedCustomTools(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-managed-tools")
	root := filepath.Join(t.TempDir(), "worker")
	registerRuntimeTestCustomTool()
	createServerWorkerWithConfig(t, ctx, s, namespace.ID, "worker", root, nil, managedAgentRuntimeConfig{
		Tools: []agent.ConfiguredTool{{
			ID: runtimeTestCustomToolName,
		}},
	})

	var (
		toolsMu sync.Mutex
		tools   []agent.ToolDefinition
	)
	manager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
			return agent.DriverFunc(func(_ context.Context, req agent.Request) (agent.Decision, error) {
				toolsMu.Lock()
				tools = append([]agent.ToolDefinition(nil), req.Tools...)
				toolsMu.Unlock()
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
		ThreadID:         "thread-managed-tools",
		RecipientAgentID: "worker",
		Payload:          "start",
		MaxAttempts:      1,
	}); err != nil {
		t.Fatalf("EnqueueMessage() error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() (bool, error) {
		toolsMu.Lock()
		defer toolsMu.Unlock()
		return len(tools) > 0, nil
	})

	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("RuntimeManager.Run() error = %v, want %v", err, context.Canceled)
	}

	toolsMu.Lock()
	captured := append([]agent.ToolDefinition(nil), tools...)
	toolsMu.Unlock()
	if len(captured) == 0 {
		t.Fatal("len(req.Tools) = 0, want built-in tools plus the custom tool")
	}
	if !hasToolNamed(captured, agent.ToolNameReadFile) {
		t.Fatalf("req.Tools = %#v, want built-in %q to remain available", captured, agent.ToolNameReadFile)
	}
	if !hasToolNamed(captured, runtimeTestCustomToolName) {
		t.Fatalf("req.Tools = %#v, want custom tool %q", captured, runtimeTestCustomToolName)
	}
}

func TestRuntimeManagerPersistsToolStepActionDetails(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-tool-step-actions")
	root := filepath.Join(t.TempDir(), "worker")
	registerRuntimeTestCustomTool()
	createServerWorkerWithConfig(t, ctx, s, namespace.ID, "worker", root, nil, managedAgentRuntimeConfig{
		Tools: []agent.ConfiguredTool{{
			ID: runtimeTestCustomToolName,
		}},
	})

	manager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
			return &scriptedDriver{
				decisions: []agent.Decision{
					{
						Thought: "use the custom skill",
						Tool: &agent.ToolAction{
							Name:  runtimeTestCustomToolName,
							Kind:  agent.ToolKindFunction,
							Input: `{}`,
						},
					},
					{Finish: &agent.FinishAction{Value: "ok"}},
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
		ThreadID:         "thread-tool-step-actions",
		RecipientAgentID: "worker",
		Payload:          "start",
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

	_, step := latestRunStepByPrompt(t, ctx, s, namespace.ID, "start")
	if step.StepType != "tool" {
		t.Fatalf("step.StepType = %q, want %q", step.StepType, "tool")
	}
	if step.ActionName != runtimeTestCustomToolName {
		t.Fatalf("step.ActionName = %q, want %q", step.ActionName, runtimeTestCustomToolName)
	}
	if step.ActionToolKind != string(agent.ToolKindFunction) {
		t.Fatalf("step.ActionToolKind = %q, want %q", step.ActionToolKind, agent.ToolKindFunction)
	}
	if step.ActionInput != `{}` {
		t.Fatalf("step.ActionInput = %q, want %q", step.ActionInput, `{}`)
	}
	if step.ActionOutput != "ok" {
		t.Fatalf("step.ActionOutput = %q, want %q", step.ActionOutput, "ok")
	}
	if step.Shell != "" {
		t.Fatalf("step.Shell = %q, want empty shell for pure tool step", step.Shell)
	}
}

func TestRuntimeManagerIncludesManagedServerTools(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-server-tools")
	root := filepath.Join(t.TempDir(), "worker")
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
				ReachableHosts: []managedAgentHostMatcher{{
					Glob: "*",
				}},
			},
			Tools: []agent.ConfiguredTool{
				{ID: "server_log"},
				{ID: "slack_post", Config: map[string]any{"default_channel": "C12345678"}},
				{ID: "slack_dm"},
				{ID: "slack_replies", Config: map[string]any{"default_channel": "C12345678"}},
				{ID: "slack_channel_history", Config: map[string]any{"default_channel": "C12345678"}},
			},
		},
	}); err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}

	var (
		toolsMu sync.Mutex
		tools   []agent.ToolDefinition
	)
	manager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
			return agent.DriverFunc(func(_ context.Context, req agent.Request) (agent.Decision, error) {
				toolsMu.Lock()
				tools = append([]agent.ToolDefinition(nil), req.Tools...)
				toolsMu.Unlock()
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
		ThreadID:         "thread-server-tools",
		RecipientAgentID: "worker",
		Payload:          "start",
		MaxAttempts:      1,
	}); err != nil {
		t.Fatalf("EnqueueMessage() error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() (bool, error) {
		toolsMu.Lock()
		defer toolsMu.Unlock()
		return len(tools) > 0, nil
	})

	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("RuntimeManager.Run() error = %v, want %v", err, context.Canceled)
	}

	toolsMu.Lock()
	captured := append([]agent.ToolDefinition(nil), tools...)
	toolsMu.Unlock()
	if !hasToolNamed(captured, "server_log") {
		t.Fatalf("req.Tools = %#v, want %q", captured, "server_log")
	}
	if !hasToolNamed(captured, "slack_post") {
		t.Fatalf("req.Tools = %#v, want %q", captured, "slack_post")
	}
	if !hasToolNamed(captured, "slack_dm") {
		t.Fatalf("req.Tools = %#v, want %q", captured, "slack_dm")
	}
	if !hasToolNamed(captured, "slack_replies") {
		t.Fatalf("req.Tools = %#v, want %q", captured, "slack_replies")
	}
	if !hasToolNamed(captured, "slack_channel_history") {
		t.Fatalf("req.Tools = %#v, want %q", captured, "slack_channel_history")
	}
}

func TestRuntimeManagerExecutesDatadogReadTool(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-datadog-tool")
	root := filepath.Join(t.TempDir(), "worker")
	if _, err := s.CreateAgent(ctx, cpstore.CreateAgentParams{
		NamespaceID:  namespace.ID,
		AgentID:      "worker",
		Name:         "worker",
		Role:         cpstore.AgentRoleWorker,
		DesiredState: cpstore.AgentDesiredStateRunning,
		RootPath:     root,
		ModelName:    "test-model",
		SystemPrompt: "You are a test worker.",
		MaxAttempts:  1,
		AllowNetwork: true,
		Config: managedAgentRuntimeConfig{
			Network: managedAgentNetworkConfig{
				ReachableHosts: []managedAgentHostMatcher{{
					Glob: "*",
				}},
			},
			Tools: []agent.ConfiguredTool{{
				ID: "datadog_read",
			}},
		},
	}); err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}

	var (
		requestsMu sync.Mutex
		requests   int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/incidents" {
			t.Fatalf("request path = %q, want %q", r.URL.Path, "/api/v2/incidents")
		}
		if got := r.URL.Query().Get("page[size]"); got != "5" {
			t.Fatalf("page[size] = %q, want %q", got, "5")
		}
		requestsMu.Lock()
		requests++
		requestsMu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{
				"id":   "inc-1",
				"type": "incidents",
				"attributes": map[string]any{
					"title": "API outage",
				},
			}},
		})
	}))
	defer server.Close()
	manager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
			if record.NamespaceID != namespace.ID || record.ID != "worker" {
				return nil, fmt.Errorf("unexpected worker %s/%s", record.NamespaceID, record.ID)
			}
			return &scriptedDriver{
				decisions: []agent.Decision{
					{Tool: &agent.ToolAction{
						Name:  "datadog_read",
						Kind:  agent.ToolKindFunction,
						Input: `{"action":"list_incidents","page_size":5}`,
					}},
					{Finish: &agent.FinishAction{Value: "ok"}},
				},
			}, nil
		}),
		PollInterval: 50 * time.Millisecond,
		EnvLookup: func(key string) string {
			switch key {
			case "DD_API_KEY":
				return "api-key"
			case "DD_APP_KEY":
				return "app-key"
			case "DD_BASE_URL":
				return server.URL
			default:
				return ""
			}
		},
	}

	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- manager.Run(runCtx)
	}()

	if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-datadog-tool",
		RecipientAgentID: "worker",
		Payload:          "start",
		MaxAttempts:      1,
	}); err != nil {
		t.Fatalf("EnqueueMessage() error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() (bool, error) {
		requestsMu.Lock()
		defer requestsMu.Unlock()
		return requests == 1, nil
	})

	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("RuntimeManager.Run() error = %v, want %v", err, context.Canceled)
	}
	requestsMu.Lock()
	got := requests
	requestsMu.Unlock()
	if got != 1 {
		t.Fatalf("request count = %d, want 1", got)
	}
}

func TestRuntimeManagerExecutesSlackChannelHistoryTool(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-slack-history-tool")
	root := filepath.Join(t.TempDir(), "worker")
	if _, err := s.CreateAgent(ctx, cpstore.CreateAgentParams{
		NamespaceID:  namespace.ID,
		AgentID:      "worker",
		Name:         "worker",
		Role:         cpstore.AgentRoleWorker,
		DesiredState: cpstore.AgentDesiredStateRunning,
		RootPath:     root,
		ModelName:    "test-model",
		SystemPrompt: "You are a test worker.",
		MaxAttempts:  1,
		AllowNetwork: true,
		Config: managedAgentRuntimeConfig{
			Network: managedAgentNetworkConfig{
				ReachableHosts: []managedAgentHostMatcher{{
					Glob: "*",
				}},
			},
			Tools: []agent.ConfiguredTool{{
				ID:     "slack_channel_history",
				Config: map[string]any{"default_channel": "C12345678"},
			}},
		},
	}); err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}

	var (
		requestsMu sync.Mutex
		requests   int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/conversations.history" {
			t.Fatalf("request path = %q, want %q", r.URL.Path, "/conversations.history")
		}
		if got := r.URL.Query().Get("channel"); got != "C12345678" {
			t.Fatalf("query channel = %q, want %q", got, "C12345678")
		}
		if got := r.URL.Query().Get("oldest"); got != "1700.000001" {
			t.Fatalf("query oldest = %q, want %q", got, "1700.000001")
		}
		if got := r.URL.Query().Get("inclusive"); got != "false" {
			t.Fatalf("query inclusive = %q, want %q", got, "false")
		}
		if got := r.URL.Query().Get("limit"); got != "1" {
			t.Fatalf("query limit = %q, want %q", got, "1")
		}
		if got := r.Header.Get("Authorization"); got != "Bearer slack-token" {
			t.Fatalf("Authorization header = %q, want bearer token", got)
		}
		requestsMu.Lock()
		requests++
		requestsMu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"messages": []map[string]any{{
				"ts":   "1700.000005",
				"type": "message",
				"text": "hello from history",
			}},
		})
	}))
	defer server.Close()

	manager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
			if record.NamespaceID != namespace.ID || record.ID != "worker" {
				return nil, fmt.Errorf("unexpected worker %s/%s", record.NamespaceID, record.ID)
			}
			return &scriptedDriver{
				decisions: []agent.Decision{
					{Tool: &agent.ToolAction{
						Name:  "slack_channel_history",
						Kind:  agent.ToolKindFunction,
						Input: `{"after_ts":"1700.000001","max_messages":1}`,
					}},
					{Finish: &agent.FinishAction{Value: "ok"}},
				},
			}, nil
		}),
		PollInterval: 50 * time.Millisecond,
		EnvLookup: func(key string) string {
			switch key {
			case "SLACK_USER_TOKEN":
				return "slack-token"
			case "SLACK_API_BASE_URL":
				return server.URL
			default:
				return ""
			}
		},
	}

	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- manager.Run(runCtx)
	}()

	if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-slack-history-tool",
		RecipientAgentID: "worker",
		Payload:          "start",
		MaxAttempts:      1,
	}); err != nil {
		t.Fatalf("EnqueueMessage() error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() (bool, error) {
		requestsMu.Lock()
		defer requestsMu.Unlock()
		return requests == 1, nil
	})
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
	requestsMu.Lock()
	got := requests
	requestsMu.Unlock()
	if got != 1 {
		t.Fatalf("request count = %d, want 1", got)
	}
	_, step := latestRunStepByPrompt(t, ctx, s, namespace.ID, "start")
	if step.ActionName != "slack_channel_history" {
		t.Fatalf("step.ActionName = %q, want %q", step.ActionName, "slack_channel_history")
	}
	if !strings.Contains(step.ActionOutput, "hello from history") {
		t.Fatalf("step.ActionOutput = %q, want Slack history response payload", step.ActionOutput)
	}
}

func TestRuntimeManagerAllowsScopedSlackToolsWhenGlobalHostDisallowed(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-slack-blocked")
	root := filepath.Join(t.TempDir(), "worker")

	var (
		requestsMu sync.Mutex
		requests   int
		toolsMu    sync.Mutex
		tools      []agent.ToolDefinition
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestsMu.Lock()
		requests++
		requestsMu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer server.Close()

	if _, err := s.CreateAgent(ctx, cpstore.CreateAgentParams{
		NamespaceID:  namespace.ID,
		AgentID:      "worker",
		Name:         "worker",
		Role:         cpstore.AgentRoleWorker,
		DesiredState: cpstore.AgentDesiredStateRunning,
		RootPath:     root,
		ModelName:    "test-model",
		SystemPrompt: "You are a test worker.",
		MaxAttempts:  1,
		AllowNetwork: true,
		Config: managedAgentRuntimeConfig{
			Network: managedAgentNetworkConfig{
				ReachableHosts: []managedAgentHostMatcher{{
					Glob: "example.com",
				}},
			},
			Tools: []agent.ConfiguredTool{
				{ID: "slack_post", Config: map[string]any{"default_channel": "C12345678"}},
				{ID: "slack_dm"},
				{ID: "slack_replies", Config: map[string]any{"default_channel": "C12345678"}},
				{ID: "slack_channel_history", Config: map[string]any{"default_channel": "C12345678"}},
			},
		},
	}); err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}

	manager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
			if record.NamespaceID != namespace.ID || record.ID != "worker" {
				return nil, fmt.Errorf("unexpected worker %s/%s", record.NamespaceID, record.ID)
			}
			driver := &scriptedDriver{
				decisions: []agent.Decision{
					{Tool: &agent.ToolAction{
						Name:  "slack_post",
						Kind:  agent.ToolKindFunction,
						Input: `{"text":"hello from test"}`,
					}},
					{Tool: &agent.ToolAction{
						Name:  "slack_dm",
						Kind:  agent.ToolKindFunction,
						Input: `{"user_id":"U12345678","text":"hello from dm"}`,
					}},
					{Tool: &agent.ToolAction{
						Name:  "slack_replies",
						Kind:  agent.ToolKindFunction,
						Input: `{"thread_ts":"1700.000001"}`,
					}},
					{Tool: &agent.ToolAction{
						Name:  "slack_channel_history",
						Kind:  agent.ToolKindFunction,
						Input: `{"after_ts":"1700.000001","max_messages":1}`,
					}},
					{Finish: &agent.FinishAction{Value: "ok"}},
				},
			}
			return agent.DriverFunc(func(ctx context.Context, req agent.Request) (agent.Decision, error) {
				toolsMu.Lock()
				tools = append([]agent.ToolDefinition(nil), req.Tools...)
				toolsMu.Unlock()
				return driver.Next(ctx, req)
			}), nil
		}),
		PollInterval: 50 * time.Millisecond,
		EnvLookup: func(key string) string {
			switch key {
			case "SLACK_USER_TOKEN":
				return "slack-token"
			case "SLACK_API_BASE_URL":
				return server.URL
			default:
				return ""
			}
		},
	}

	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- manager.Run(runCtx)
	}()

	if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-slack-blocked",
		RecipientAgentID: "worker",
		Payload:          "start",
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

	requestsMu.Lock()
	gotRequests := requests
	requestsMu.Unlock()
	if gotRequests != 4 {
		t.Fatalf("request count = %d, want 4 for scoped Slack tools", gotRequests)
	}
	toolsMu.Lock()
	captured := append([]agent.ToolDefinition(nil), tools...)
	toolsMu.Unlock()
	if !hasToolNamed(captured, "slack_post") {
		t.Fatalf("req.Tools = %#v, want %q", captured, "slack_post")
	}
	if !hasToolNamed(captured, "slack_dm") {
		t.Fatalf("req.Tools = %#v, want %q", captured, "slack_dm")
	}
	if !hasToolNamed(captured, "slack_replies") {
		t.Fatalf("req.Tools = %#v, want %q", captured, "slack_replies")
	}
	if !hasToolNamed(captured, "slack_channel_history") {
		t.Fatalf("req.Tools = %#v, want %q", captured, "slack_channel_history")
	}
}

func TestRuntimeManagerAllowsScopedDatadogReadToolWhenGlobalHostDisallowed(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-datadog-blocked")
	root := filepath.Join(t.TempDir(), "worker")

	var (
		requestsMu sync.Mutex
		requests   int
		toolsMu    sync.Mutex
		tools      []agent.ToolDefinition
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestsMu.Lock()
		requests++
		requestsMu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	}))
	defer server.Close()

	if _, err := s.CreateAgent(ctx, cpstore.CreateAgentParams{
		NamespaceID:  namespace.ID,
		AgentID:      "worker",
		Name:         "worker",
		Role:         cpstore.AgentRoleWorker,
		DesiredState: cpstore.AgentDesiredStateRunning,
		RootPath:     root,
		ModelName:    "test-model",
		SystemPrompt: "You are a test worker.",
		MaxAttempts:  1,
		AllowNetwork: true,
		Config: managedAgentRuntimeConfig{
			Network: managedAgentNetworkConfig{
				ReachableHosts: []managedAgentHostMatcher{{
					Glob: "example.com",
				}},
			},
			Tools: []agent.ConfiguredTool{{
				ID: "datadog_read",
			}},
		},
	}); err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}

	manager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
			if record.NamespaceID != namespace.ID || record.ID != "worker" {
				return nil, fmt.Errorf("unexpected worker %s/%s", record.NamespaceID, record.ID)
			}
			driver := &scriptedDriver{
				decisions: []agent.Decision{
					{Tool: &agent.ToolAction{
						Name:  "datadog_read",
						Kind:  agent.ToolKindFunction,
						Input: `{"action":"list_incidents","page_size":5}`,
					}},
					{Finish: &agent.FinishAction{Value: "ok"}},
				},
			}
			return agent.DriverFunc(func(ctx context.Context, req agent.Request) (agent.Decision, error) {
				toolsMu.Lock()
				tools = append([]agent.ToolDefinition(nil), req.Tools...)
				toolsMu.Unlock()
				return driver.Next(ctx, req)
			}), nil
		}),
		PollInterval: 50 * time.Millisecond,
		EnvLookup: func(key string) string {
			switch key {
			case "DD_API_KEY":
				return "api-key"
			case "DD_APP_KEY":
				return "app-key"
			case "DD_BASE_URL":
				return server.URL
			default:
				return ""
			}
		},
	}

	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- manager.Run(runCtx)
	}()

	if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-datadog-blocked",
		RecipientAgentID: "worker",
		Payload:          "start",
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

	requestsMu.Lock()
	gotRequests := requests
	requestsMu.Unlock()
	if gotRequests != 1 {
		t.Fatalf("request count = %d, want 1 for scoped Datadog tool", gotRequests)
	}
	toolsMu.Lock()
	captured := append([]agent.ToolDefinition(nil), tools...)
	toolsMu.Unlock()
	if !hasToolNamed(captured, "datadog_read") {
		t.Fatalf("req.Tools = %#v, want %q", captured, "datadog_read")
	}
}

func TestRuntimeManagerExecutesGitHubReadRepoTool(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-github-repo-tool")
	root := filepath.Join(t.TempDir(), "worker")
	if _, err := s.CreateAgent(ctx, cpstore.CreateAgentParams{
		NamespaceID:  namespace.ID,
		AgentID:      "worker",
		Name:         "worker",
		Role:         cpstore.AgentRoleWorker,
		DesiredState: cpstore.AgentDesiredStateRunning,
		RootPath:     root,
		ModelName:    "test-model",
		SystemPrompt: "You are a test worker.",
		MaxAttempts:  1,
		AllowNetwork: true,
		Config: managedAgentRuntimeConfig{
			Network: managedAgentNetworkConfig{
				ReachableHosts: []managedAgentHostMatcher{{Glob: "*"}},
			},
			Tools: []agent.ConfiguredTool{{
				ID: "github_read_repo",
			}},
		},
	}); err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}

	var (
		requestsMu sync.Mutex
		requests   int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/monorepo" {
			t.Fatalf("request path = %q, want repo path", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer github-token" {
			t.Fatalf("Authorization = %q, want bearer token", got)
		}
		requestsMu.Lock()
		requests++
		requestsMu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"full_name":      "acme/monorepo",
			"default_branch": "main",
			"visibility":     "private",
			"archived":       false,
			"topics":         []string{"platform", "mono"},
			"license": map[string]any{
				"key":  "apache-2.0",
				"name": "Apache License 2.0",
			},
		})
	}))
	defer server.Close()

	manager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
			return &scriptedDriver{
				decisions: []agent.Decision{
					{Tool: &agent.ToolAction{
						Name:  "github_read_repo",
						Kind:  agent.ToolKindFunction,
						Input: `{"action":"get_repo","owner":"acme","repo":"monorepo"}`,
					}},
					{Finish: &agent.FinishAction{Value: "ok"}},
				},
			}, nil
		}),
		PollInterval: 50 * time.Millisecond,
		EnvLookup: func(key string) string {
			switch key {
			case "GITHUB_TOKEN":
				return "github-token"
			case "GITHUB_API_BASE_URL":
				return server.URL
			default:
				return ""
			}
		},
	}

	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- manager.Run(runCtx)
	}()

	if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-github-repo-tool",
		RecipientAgentID: "worker",
		Payload:          "start",
		MaxAttempts:      1,
	}); err != nil {
		t.Fatalf("EnqueueMessage() error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() (bool, error) {
		requestsMu.Lock()
		defer requestsMu.Unlock()
		return requests == 1, nil
	})
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
	_, step := latestRunStepByPrompt(t, ctx, s, namespace.ID, "start")
	if step.ActionName != "github_read_repo" {
		t.Fatalf("step.ActionName = %q, want %q", step.ActionName, "github_read_repo")
	}
	if !strings.Contains(step.ActionOutput, `"default_branch":"main"`) {
		t.Fatalf("step.ActionOutput = %q, want repo response payload", step.ActionOutput)
	}
}

func TestRuntimeManagerExecutesGitHubReadReviewsTool(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-github-reviews-tool")
	root := filepath.Join(t.TempDir(), "worker")
	if _, err := s.CreateAgent(ctx, cpstore.CreateAgentParams{
		NamespaceID:  namespace.ID,
		AgentID:      "worker",
		Name:         "worker",
		Role:         cpstore.AgentRoleWorker,
		DesiredState: cpstore.AgentDesiredStateRunning,
		RootPath:     root,
		ModelName:    "test-model",
		SystemPrompt: "You are a test worker.",
		MaxAttempts:  1,
		AllowNetwork: true,
		Config: managedAgentRuntimeConfig{
			Network: managedAgentNetworkConfig{
				ReachableHosts: []managedAgentHostMatcher{{Glob: "*"}},
			},
			Tools: []agent.ConfiguredTool{{
				ID: "github_read_reviews",
			}},
		},
	}); err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}

	var (
		requestsMu sync.Mutex
		requests   int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/issues" {
			t.Fatalf("request path = %q, want search issues path", r.URL.Path)
		}
		if got := r.URL.Query().Get("q"); !strings.Contains(got, "repo:acme/monorepo") {
			t.Fatalf("query q = %q, want repo qualifier", got)
		}
		requestsMu.Lock()
		requests++
		requestsMu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"total_count":        1,
			"incomplete_results": false,
			"items": []map[string]any{{
				"number": 4182,
				"title":  "Flaky integration test in payments",
				"state":  "open",
				"labels": []map[string]any{{"name": "flaky-test"}},
			}},
		})
	}))
	defer server.Close()

	manager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
			return &scriptedDriver{
				decisions: []agent.Decision{
					{Tool: &agent.ToolAction{
						Name:  "github_read_reviews",
						Kind:  agent.ToolKindFunction,
						Input: `{"action":"search_issues","owner":"acme","repo":"monorepo","query":"label:flaky-test state:open","page":1,"per_page":20}`,
					}},
					{Finish: &agent.FinishAction{Value: "ok"}},
				},
			}, nil
		}),
		PollInterval: 50 * time.Millisecond,
		EnvLookup: func(key string) string {
			switch key {
			case "GITHUB_TOKEN":
				return "github-token"
			case "GITHUB_API_BASE_URL":
				return server.URL
			default:
				return ""
			}
		},
	}

	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- manager.Run(runCtx)
	}()

	if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-github-reviews-tool",
		RecipientAgentID: "worker",
		Payload:          "start",
		MaxAttempts:      1,
	}); err != nil {
		t.Fatalf("EnqueueMessage() error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() (bool, error) {
		requestsMu.Lock()
		defer requestsMu.Unlock()
		return requests == 1, nil
	})
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
	_, step := latestRunStepByPrompt(t, ctx, s, namespace.ID, "start")
	if step.ActionName != "github_read_reviews" {
		t.Fatalf("step.ActionName = %q, want %q", step.ActionName, "github_read_reviews")
	}
	if !strings.Contains(step.ActionOutput, `Flaky integration test in payments`) {
		t.Fatalf("step.ActionOutput = %q, want issue search payload", step.ActionOutput)
	}
}

func TestRuntimeManagerExecutesGitHubReadCIToolDownloadArtifact(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-github-ci-tool")
	root := filepath.Join(t.TempDir(), "worker")
	if _, err := s.CreateAgent(ctx, cpstore.CreateAgentParams{
		NamespaceID:  namespace.ID,
		AgentID:      "worker",
		Name:         "worker",
		Role:         cpstore.AgentRoleWorker,
		DesiredState: cpstore.AgentDesiredStateRunning,
		RootPath:     root,
		ModelName:    "test-model",
		SystemPrompt: "You are a test worker.",
		MaxAttempts:  1,
		AllowNetwork: true,
		Config: managedAgentRuntimeConfig{
			Network: managedAgentNetworkConfig{
				ReachableHosts: []managedAgentHostMatcher{{Glob: "*"}},
			},
			Tools: []agent.ConfiguredTool{{
				ID: "github_read_ci",
			}},
		},
	}); err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}

	zipBytes := mustRuntimeZIP(t, map[string]string{"junit.xml": "<testsuite/>"})
	var (
		requestsMu sync.Mutex
		requests   int
		serverURL  string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestsMu.Lock()
		requests++
		requestsMu.Unlock()
		switch r.URL.Path {
		case "/repos/acme/monorepo/actions/artifacts/701":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":            701,
				"name":          "junit-results",
				"size_in_bytes": len(zipBytes),
				"expired":       false,
				"expires_at":    "2026-06-18T00:00:00Z",
				"workflow_run": map[string]any{
					"id":          123,
					"head_branch": "main",
					"head_sha":    "abc123",
				},
			})
		case "/repos/acme/monorepo/actions/artifacts/701/zip":
			http.Redirect(w, r, serverURL+"/downloads/701.zip", http.StatusFound)
		case "/downloads/701.zip":
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write(zipBytes)
		default:
			t.Fatalf("unexpected request path %q", r.URL.Path)
		}
	}))
	defer server.Close()
	serverURL = server.URL

	manager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
			return &scriptedDriver{
				decisions: []agent.Decision{
					{Tool: &agent.ToolAction{
						Name:  "github_read_ci",
						Kind:  agent.ToolKindFunction,
						Input: `{"action":"download_artifact","owner":"acme","repo":"monorepo","artifact_id":701,"extract":true}`,
					}},
					{Finish: &agent.FinishAction{Value: "ok"}},
				},
			}, nil
		}),
		PollInterval: 50 * time.Millisecond,
		EnvLookup: func(key string) string {
			switch key {
			case "GITHUB_TOKEN":
				return "github-token"
			case "GITHUB_API_BASE_URL":
				return server.URL
			default:
				return ""
			}
		},
	}

	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- manager.Run(runCtx)
	}()

	if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-github-ci-tool",
		RecipientAgentID: "worker",
		Payload:          "start",
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
	requestsMu.Lock()
	gotRequests := requests
	requestsMu.Unlock()
	if gotRequests < 3 {
		t.Fatalf("request count = %d, want at least 3 for metadata, redirect, and download", gotRequests)
	}
	extractedPath := filepath.Join(root, "github/actions/artifacts/701/junit.xml")
	payload, err := os.ReadFile(extractedPath)
	if err != nil {
		t.Fatalf("os.ReadFile(extractedPath) error = %v", err)
	}
	if string(payload) != "<testsuite/>" {
		t.Fatalf("extracted payload = %q, want junit payload", string(payload))
	}
	_, step := latestRunStepByPrompt(t, ctx, s, namespace.ID, "start")
	if step.ActionName != "github_read_ci" {
		t.Fatalf("step.ActionName = %q, want %q", step.ActionName, "github_read_ci")
	}
	if !strings.Contains(step.ActionOutput, `"redirected":true`) {
		t.Fatalf("step.ActionOutput = %q, want redirected artifact metadata", step.ActionOutput)
	}
}

func TestRuntimeManagerAllowsScopedGitHubReadRepoToolWhenGlobalHostDisallowed(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newServerStore(t)
	namespace := createServerNamespace(t, ctx, s, "namespace-github-repo-blocked")
	root := filepath.Join(t.TempDir(), "worker")

	var (
		requestsMu sync.Mutex
		requests   int
		toolsMu    sync.Mutex
		tools      []agent.ToolDefinition
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestsMu.Lock()
		requests++
		requestsMu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"full_name": "acme/monorepo"})
	}))
	defer server.Close()

	if _, err := s.CreateAgent(ctx, cpstore.CreateAgentParams{
		NamespaceID:  namespace.ID,
		AgentID:      "worker",
		Name:         "worker",
		Role:         cpstore.AgentRoleWorker,
		DesiredState: cpstore.AgentDesiredStateRunning,
		RootPath:     root,
		ModelName:    "test-model",
		SystemPrompt: "You are a test worker.",
		MaxAttempts:  1,
		AllowNetwork: true,
		Config: managedAgentRuntimeConfig{
			Network: managedAgentNetworkConfig{
				ReachableHosts: []managedAgentHostMatcher{{Glob: "example.com"}},
			},
			Tools: []agent.ConfiguredTool{{
				ID: "github_read_repo",
			}},
		},
	}); err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}

	manager := &RuntimeManager{
		Store: s,
		DriverFactory: driverFactoryFunc(func(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
			driver := &scriptedDriver{
				decisions: []agent.Decision{
					{Tool: &agent.ToolAction{
						Name:  "github_read_repo",
						Kind:  agent.ToolKindFunction,
						Input: `{"action":"get_repo","owner":"acme","repo":"monorepo"}`,
					}},
					{Finish: &agent.FinishAction{Value: "ok"}},
				},
			}
			return agent.DriverFunc(func(ctx context.Context, req agent.Request) (agent.Decision, error) {
				toolsMu.Lock()
				tools = append([]agent.ToolDefinition(nil), req.Tools...)
				toolsMu.Unlock()
				return driver.Next(ctx, req)
			}), nil
		}),
		PollInterval: 50 * time.Millisecond,
		EnvLookup: func(key string) string {
			switch key {
			case "GITHUB_TOKEN":
				return "github-token"
			case "GITHUB_API_BASE_URL":
				return server.URL
			default:
				return ""
			}
		},
	}

	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- manager.Run(runCtx)
	}()

	if _, err := s.EnqueueMessage(ctx, cpstore.CreateMailboxMessageParams{
		NamespaceID:      namespace.ID,
		ThreadID:         "thread-github-repo-blocked",
		RecipientAgentID: "worker",
		Payload:          "start",
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
	requestsMu.Lock()
	gotRequests := requests
	requestsMu.Unlock()
	if gotRequests != 1 {
		t.Fatalf("request count = %d, want 1 for scoped GitHub tool", gotRequests)
	}
	toolsMu.Lock()
	captured := append([]agent.ToolDefinition(nil), tools...)
	toolsMu.Unlock()
	if !hasToolNamed(captured, "github_read_repo") {
		t.Fatalf("req.Tools = %#v, want %q", captured, "github_read_repo")
	}
}

func mustRuntimeZIP(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, contents := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("writer.Create(%q) error = %v", name, err)
		}
		if _, err := entry.Write([]byte(contents)); err != nil {
			t.Fatalf("entry.Write(%q) error = %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close() error = %v", err)
	}
	return buffer.Bytes()
}

func hasToolNamed(tools []agent.ToolDefinition, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}
