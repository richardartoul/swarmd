package server

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/richardartoul/swarmd/pkg/agent"
	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

const (
	specsTestCustomToolOne = "specs_test_custom_tool_one"
	specsTestCustomToolTwo = "specs_test_custom_tool_two"
)

var registerSpecsTestCustomToolsOnce sync.Once

func registerSpecsTestCustomTools() {
	registerSpecsTestCustomToolsOnce.Do(func() {
		agent.RegisterTool(runtimeTestToolPlugin{
			definition: agent.ToolDefinition{
				Name:        specsTestCustomToolOne,
				Description: "specs test custom tool one",
				Kind:        agent.ToolKindFunction,
				Parameters: map[string]any{
					"type":                 "object",
					"properties":           map[string]any{},
					"additionalProperties": false,
				},
			},
		})
		agent.RegisterTool(runtimeTestToolPlugin{
			definition: agent.ToolDefinition{
				Name:        specsTestCustomToolTwo,
				Description: "specs test custom tool two",
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

func writeAgentSpec(t *testing.T, configRoot, namespaceID, agentID, contents string) {
	t.Helper()
	dir := filepath.Join(configRoot, "namespaces", namespaceID, "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", dir, err)
	}
	path := filepath.Join(dir, agentID+".yaml")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", path, err)
	}
}

func decodeManagedAgentMemory(t *testing.T, configJSON string) AgentMemorySpec {
	t.Helper()
	var config struct {
		Memory AgentMemorySpec `json:"memory"`
	}
	if err := cpstore.DecodeEnvelopeInto(configJSON, &config); err != nil {
		t.Fatalf("DecodeEnvelopeInto() error = %v", err)
	}
	return config.Memory
}

func mustManagedMountContent(t *testing.T, mount managedAgentMount) []byte {
	t.Helper()
	data, err := mount.contentBytes()
	if err != nil {
		t.Fatalf("mount.contentBytes() error = %v", err)
	}
	return data
}

func findManagedMount(t *testing.T, mounts []managedAgentMount, path string) managedAgentMount {
	t.Helper()
	for _, mount := range mounts {
		if mount.Path == path {
			return mount
		}
	}
	t.Fatalf("missing managed mount %q in %#v", path, mounts)
	return managedAgentMount{}
}
