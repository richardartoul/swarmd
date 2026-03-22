package registry

import (
	"context"
	"testing"

	"github.com/richardartoul/swarmd/pkg/sh/interp"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
)

func TestRegisteredCustomToolsReturnsStableMetadata(t *testing.T) {
	restoreRegistryState := swapTestRegistryState()
	defer restoreRegistryState()

	if err := RegisterBuiltIn(testToolPlugin{
		definition: toolscore.ToolDefinition{
			Name: "built_in_tool",
		},
	}); err != nil {
		t.Fatalf("RegisterBuiltIn() error = %v", err)
	}
	if err := Register(testToolPlugin{
		definition: toolscore.ToolDefinition{
			Name:         "z_custom_tool",
			NetworkScope: toolscore.ToolNetworkScopeScoped,
		},
	}, RegistrationOptions{
		RequiredEnv:   []string{"Z_TOKEN"},
		RequiredHosts: []interp.HostMatcher{{Glob: "api.example.com"}},
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if err := Register(testToolPlugin{
		definition: toolscore.ToolDefinition{
			Name: "a_custom_tool",
		},
	}, RegistrationOptions{
		RequiredEnv: []string{"A_TOKEN"},
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	got := RegisteredCustomTools()
	if len(got) != 2 {
		t.Fatalf("len(RegisteredCustomTools()) = %d, want 2", len(got))
	}
	if got[0].Definition.Name != "a_custom_tool" || got[1].Definition.Name != "z_custom_tool" {
		t.Fatalf("RegisteredCustomTools() names = [%q %q], want sorted custom tools", got[0].Definition.Name, got[1].Definition.Name)
	}
	if len(got[0].RequiredEnv) != 1 || got[0].RequiredEnv[0] != "A_TOKEN" {
		t.Fatalf("first RequiredEnv = %#v, want [A_TOKEN]", got[0].RequiredEnv)
	}
	if len(got[1].RequiredHosts) != 1 || got[1].RequiredHosts[0].Glob != "api.example.com" {
		t.Fatalf("second RequiredHosts = %#v, want api.example.com", got[1].RequiredHosts)
	}

	got[0].RequiredEnv[0] = "changed"
	got[1].RequiredHosts[0] = interp.HostMatcher{Glob: "mutated.example.com"}

	again := RegisteredCustomTools()
	if again[0].RequiredEnv[0] != "A_TOKEN" {
		t.Fatalf("RegisteredCustomTools() RequiredEnv was mutated = %#v", again[0].RequiredEnv)
	}
	if again[1].RequiredHosts[0].Glob != "api.example.com" {
		t.Fatalf("RegisteredCustomTools() RequiredHosts was mutated = %#v", again[1].RequiredHosts)
	}
}

type testToolPlugin struct {
	definition toolscore.ToolDefinition
}

func (p testToolPlugin) Definition() toolscore.ToolDefinition {
	return p.definition
}

func (p testToolPlugin) NewHandler(toolscore.ConfiguredTool) (toolscore.ToolHandler, error) {
	return toolscore.ToolHandlerFunc(func(context.Context, toolscore.ToolContext, *toolscore.Step, *toolscore.ToolAction) error {
		return nil
	}), nil
}

func swapTestRegistryState() func() {
	registryMu.Lock()
	previousRegistry := toolRegistry
	previousBuiltIns := builtInToolOrder
	toolRegistry = make(map[string]toolRegistration)
	builtInToolOrder = nil
	registryMu.Unlock()
	return func() {
		registryMu.Lock()
		toolRegistry = previousRegistry
		builtInToolOrder = previousBuiltIns
		registryMu.Unlock()
	}
}
