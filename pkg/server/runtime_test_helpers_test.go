package server

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/richardartoul/swarmd/pkg/agent"
	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

const runtimeTestCustomToolName = "runtime_test_custom_tool"

var registerRuntimeTestCustomToolOnce sync.Once

func registerRuntimeTestCustomTool() {
	registerRuntimeTestCustomToolOnce.Do(func() {
		agent.RegisterTool(runtimeTestToolPlugin{
			definition: agent.ToolDefinition{
				Name:        runtimeTestCustomToolName,
				Description: "test-only custom tool",
				Kind:        agent.ToolKindFunction,
				Parameters: map[string]any{
					"type":                 "object",
					"properties":           map[string]any{},
					"additionalProperties": false,
				},
			},
		})
	})
}

type runtimeTestToolPlugin struct {
	definition agent.ToolDefinition
}

func (p runtimeTestToolPlugin) Definition() agent.ToolDefinition {
	return p.definition
}

func (p runtimeTestToolPlugin) NewHandler(config agent.ConfiguredTool) (agent.ToolHandler, error) {
	return agent.ToolHandlerFunc(func(ctx context.Context, toolCtx agent.ToolContext, step *agent.Step, call *agent.ToolAction) error {
		toolCtx.SetOutput(step, "ok")
		return nil
	}), nil
}

type driverFactoryFunc func(ctx context.Context, record cpstore.RunnableAgent) (agent.Driver, error)

func (f driverFactoryFunc) NewWorkerDriver(ctx context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
	return f(ctx, record)
}

type scriptedDriver struct {
	mu        sync.Mutex
	decisions []agent.Decision
	index     int
}

func (d *scriptedDriver) Next(context.Context, agent.Request) (agent.Decision, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.index >= len(d.decisions) {
		return agent.Decision{}, fmt.Errorf("unexpected extra decision request")
	}
	decision := d.decisions[d.index]
	d.index++
	return decision, nil
}

func newServerStore(t *testing.T) *cpstore.Store {
	t.Helper()
	s, err := cpstore.Open(context.Background(), filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Fatalf("store.Close() error = %v", err)
		}
	})
	return s
}

func createServerNamespace(t *testing.T, ctx context.Context, s *cpstore.Store, name string) cpstore.Namespace {
	t.Helper()
	namespace, err := s.CreateNamespace(ctx, cpstore.CreateNamespaceParams{Name: name})
	if err != nil {
		t.Fatalf("CreateNamespace() error = %v", err)
	}
	return namespace
}

func createServerWorker(t *testing.T, ctx context.Context, s *cpstore.Store, namespaceID, agentID, root string) cpstore.RunnableAgent {
	return createServerWorkerWithConfig(t, ctx, s, namespaceID, agentID, root, nil, nil)
}

func createServerWorkerWithCommands(t *testing.T, ctx context.Context, s *cpstore.Store, namespaceID, agentID, root string, sandboxCommands []string) cpstore.RunnableAgent {
	return createServerWorkerWithConfig(t, ctx, s, namespaceID, agentID, root, sandboxCommands, nil)
}

func createServerWorkerWithConfig(t *testing.T, ctx context.Context, s *cpstore.Store, namespaceID, agentID, root string, sandboxCommands []string, config any) cpstore.RunnableAgent {
	t.Helper()
	if len(sandboxCommands) > 0 {
		legacyTools := make([]agent.ConfiguredTool, 0, len(sandboxCommands))
		for _, toolID := range sandboxCommands {
			legacyTools = append(legacyTools, agent.ConfiguredTool{ID: toolID})
		}
		switch typed := config.(type) {
		case nil:
			config = managedAgentRuntimeConfig{Tools: legacyTools}
		case managedAgentRuntimeConfig:
			typed.Tools = append(append([]agent.ConfiguredTool(nil), typed.Tools...), legacyTools...)
			config = typed
		default:
			t.Fatalf("unsupported config type with legacy sandboxCommands helper: %T", config)
		}
	}
	record, err := s.CreateAgent(ctx, cpstore.CreateAgentParams{
		NamespaceID:  namespaceID,
		AgentID:      agentID,
		Name:         agentID,
		Role:         cpstore.AgentRoleWorker,
		DesiredState: cpstore.AgentDesiredStateRunning,
		RootPath:     root,
		ModelName:    "test-model",
		SystemPrompt: "You are a test worker.",
		MaxAttempts:  3,
		Config:       config,
	})
	if err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}
	return record
}

func waitForCondition(t *testing.T, timeout time.Duration, check func() (bool, error)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		ok, err := check()
		if err != nil {
			t.Fatalf("waitForCondition check error = %v", err)
		}
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("condition not satisfied before timeout")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
