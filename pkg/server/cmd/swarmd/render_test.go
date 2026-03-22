package main

import (
	"bytes"
	"strings"
	"testing"

	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

func TestRenderAgentShowRendersToolsFromConfig(t *testing.T) {
	t.Parallel()

	configJSON, err := cpstore.MarshalOptionalEnvelope("agent_config", map[string]any{
		"tools": []map[string]any{{
			"id": "server_log",
		}},
	})
	if err != nil {
		t.Fatalf("MarshalOptionalEnvelope() error = %v", err)
	}

	var out bytes.Buffer
	renderAgentShow(&out, agentShowData{
		Agent: cpstore.RunnableAgent{
			AgentRecord: cpstore.AgentRecord{
				NamespaceID: "default",
				ID:          "worker",
				Name:        "worker",
				RootPath:    "/tmp/worker",
				ModelName:   "test-model",
				ConfigJSON:  configJSON,
			},
			SystemPrompt: "You are a test worker.",
		},
	})

	if got := out.String(); !strings.Contains(got, "config:") {
		t.Fatalf("renderAgentShow() output = %q, want config block", got)
	}
	if got := out.String(); !strings.Contains(got, `"id": "server_log"`) {
		t.Fatalf("renderAgentShow() output = %q, want server_log tool entry", got)
	}
	if got := out.String(); strings.Contains(got, "sandbox_commands:") {
		t.Fatalf("renderAgentShow() output = %q, want deprecated sandbox_commands block removed", got)
	}
}

func TestFormatRootWithFilesystemKindMarksMemoryRoots(t *testing.T) {
	t.Parallel()

	configJSON, err := cpstore.MarshalOptionalEnvelope("agent_config", map[string]any{
		"filesystem": map[string]any{
			"kind": "memory",
		},
	})
	if err != nil {
		t.Fatalf("MarshalOptionalEnvelope() error = %v", err)
	}

	if got := formatRootWithFilesystemKind("/", configJSON); got != "/ (memory)" {
		t.Fatalf("formatRootWithFilesystemKind() = %q, want %q", got, "/ (memory)")
	}
}

func TestRenderAgentShowDerivesGlobalNetworkFromConfig(t *testing.T) {
	t.Parallel()

	configJSON, err := cpstore.MarshalOptionalEnvelope("agent_config", map[string]any{
		"network": map[string]any{
			"reachable_hosts": []map[string]any{{
				"glob": "*",
			}},
		},
	})
	if err != nil {
		t.Fatalf("MarshalOptionalEnvelope() error = %v", err)
	}

	var out bytes.Buffer
	renderAgentShow(&out, agentShowData{
		Agent: cpstore.RunnableAgent{
			AgentRecord: cpstore.AgentRecord{
				NamespaceID: "default",
				ID:          "worker",
				Name:        "worker",
				RootPath:    "/tmp/worker",
				ModelName:   "test-model",
				ConfigJSON:  configJSON,
			},
			SystemPrompt: "You are a test worker.",
		},
	})

	if got := out.String(); !strings.Contains(got, "global_network_enabled: true") {
		t.Fatalf("renderAgentShow() output = %q, want derived global network flag", got)
	}
}
