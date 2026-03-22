package server

import (
	"strings"
	"testing"

	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

func TestComposeManagedSystemPromptIncludesMailboxGuidance(t *testing.T) {
	t.Parallel()

	got := composeManagedSystemPrompt(cpstore.RunnableAgent{
		SystemPrompt: "Handle the current trigger carefully.",
	}, map[string]any{
		capabilityAllowMessageSend: true,
	}, AgentMemorySpec{Disable: true}, nil, managedAgentNetworkConfig{}, nil)

	if !strings.Contains(got, "Agent-to-agent messaging:") {
		t.Fatalf("composeManagedSystemPrompt() = %q, want mailbox guidance header", got)
	}
	if !strings.Contains(got, `"outbox"`) {
		t.Fatalf("composeManagedSystemPrompt() = %q, want outbox guidance", got)
	}
	if !strings.Contains(got, `"recipient_agent_id"`) {
		t.Fatalf("composeManagedSystemPrompt() = %q, want recipient guidance", got)
	}
	if !strings.Contains(got, `"finish" response`) {
		t.Fatalf("composeManagedSystemPrompt() = %q, want finish-result guidance", got)
	}
}

func TestComposeManagedSystemPromptOmitsMailboxGuidanceWhenCapabilityDisabled(t *testing.T) {
	t.Parallel()

	got := composeManagedSystemPrompt(cpstore.RunnableAgent{
		SystemPrompt: "Handle the current trigger carefully.",
	}, nil, AgentMemorySpec{Disable: true}, nil, managedAgentNetworkConfig{}, nil)

	if strings.Contains(got, "Agent-to-agent messaging:") {
		t.Fatalf("composeManagedSystemPrompt() = %q, want mailbox guidance omitted", got)
	}
	if strings.Contains(got, `"recipient_agent_id"`) {
		t.Fatalf("composeManagedSystemPrompt() = %q, want mailbox payload guidance omitted", got)
	}
}

func TestComposeManagedSystemPromptIncludesHTTPHeaderGuidance(t *testing.T) {
	t.Parallel()

	value := "secret-http-header-value"
	got := composeManagedSystemPrompt(cpstore.RunnableAgent{
		AgentRecord:  cpstore.AgentRecord{},
		SystemPrompt: "Handle the current trigger carefully.",
	}, nil, AgentMemorySpec{Disable: true}, nil, managedAgentNetworkConfig{
		ReachableHosts: []managedAgentHostMatcher{{Glob: "*"}},
	}, []managedAgentHTTPHeader{{
		Name:  "Authorization",
		Value: &value,
		Domains: []managedAgentHTTPDomainMatcher{{
			Glob: "*.example.com",
		}},
	}})

	if !strings.Contains(got, "Automatic HTTP headers:") {
		t.Fatalf("composeManagedSystemPrompt() = %q, want HTTP header guidance header", got)
	}
	if !strings.Contains(got, "Authorization") {
		t.Fatalf("composeManagedSystemPrompt() = %q, want header name guidance", got)
	}
	if !strings.Contains(got, "glob:*.example.com") {
		t.Fatalf("composeManagedSystemPrompt() = %q, want domain scope guidance", got)
	}
	if strings.Contains(got, value) {
		t.Fatalf("composeManagedSystemPrompt() = %q, want header value hidden", got)
	}
}

func TestComposeManagedSystemPromptIncludesNetworkGuidance(t *testing.T) {
	t.Parallel()

	got := composeManagedSystemPrompt(cpstore.RunnableAgent{
		AgentRecord:  cpstore.AgentRecord{},
		SystemPrompt: "Handle the current trigger carefully.",
	}, nil, AgentMemorySpec{Disable: true}, nil, managedAgentNetworkConfig{
		ReachableHosts: []managedAgentHostMatcher{{
			Glob: "*.example.com",
		}, {
			Regex: "api-[a-z0-9-]+\\.corp\\.internal",
		}},
	}, nil)

	if !strings.Contains(got, "Network policy:") {
		t.Fatalf("composeManagedSystemPrompt() = %q, want network guidance header", got)
	}
	if !strings.Contains(got, "glob:*.example.com") {
		t.Fatalf("composeManagedSystemPrompt() = %q, want glob network scope guidance", got)
	}
	if !strings.Contains(got, "regex:api-[a-z0-9-]+\\.corp\\.internal") {
		t.Fatalf("composeManagedSystemPrompt() = %q, want regex network scope guidance", got)
	}
}

func TestComposeManagedSystemPromptIncludesLargeToolOutputGuidance(t *testing.T) {
	t.Parallel()

	got := composeManagedSystemPrompt(cpstore.RunnableAgent{
		SystemPrompt: "Handle the current trigger carefully.",
	}, nil, AgentMemorySpec{Disable: true}, nil, managedAgentNetworkConfig{}, nil)

	if !strings.Contains(got, "Large tool outputs:") {
		t.Fatalf("composeManagedSystemPrompt() = %q, want large-output guidance header", got)
	}
	if !strings.Contains(got, "These runtime spill files are temporary and are separate from pre-run mounts.") {
		t.Fatalf("composeManagedSystemPrompt() = %q, want mount-separation guidance", got)
	}
}

func TestMemoryPromptGuidanceEncouragesPersistingUsefulLearnings(t *testing.T) {
	t.Parallel()

	got := memoryPromptGuidance(AgentMemorySpec{})
	if !strings.Contains(got, "If you learn something interesting or useful that could help with subsequent runs, consider recording it in the relevant memory file.") {
		t.Fatalf("memoryPromptGuidance() = %q, want reminder to persist useful learnings", got)
	}
}
