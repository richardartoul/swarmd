package server

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/richardartoul/swarmd/pkg/agent"
	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

const (
	defaultAgentMemoryDir            = ".memory"
	defaultAgentMemoryRootFile       = "ROOT.md"
	capabilityAllowMessageSend       = "allow_message_send"
	managedAgentFilesystemKindDisk   = "disk"
	managedAgentFilesystemKindMemory = "memory"
)

type managedAgentRuntimeConfig struct {
	Capabilities             map[string]any               `json:"capabilities,omitempty"`
	Tools                    []agent.ConfiguredTool       `json:"tools,omitempty"`
	Filesystem               managedAgentFilesystemConfig `json:"filesystem,omitempty"`
	Memory                   AgentMemorySpec              `json:"memory"`
	Mounts                   []managedAgentMount          `json:"mounts,omitempty"`
	Network                  managedAgentNetworkConfig    `json:"network,omitempty"`
	HTTP                     managedAgentHTTPConfig       `json:"http,omitempty"`
	OutputFileThresholdBytes int                          `json:"output_file_threshold_bytes,omitempty"`
}

type managedAgentFilesystemConfig struct {
	Kind string `json:"kind,omitempty"`
}

func (c managedAgentRuntimeConfig) memorySettings() AgentMemorySpec {
	return c.Memory
}

func (c managedAgentRuntimeConfig) filesystemSettings() managedAgentFilesystemConfig {
	return c.Filesystem.normalized()
}

func (c managedAgentRuntimeConfig) mountSettings() []managedAgentMount {
	return append([]managedAgentMount(nil), c.Mounts...)
}

func (c managedAgentRuntimeConfig) capabilities() map[string]any {
	if len(c.Capabilities) == 0 {
		return nil
	}
	capabilities := make(map[string]any, len(c.Capabilities))
	for key, value := range c.Capabilities {
		capabilities[key] = value
	}
	return capabilities
}

func (c managedAgentRuntimeConfig) toolSettings() []agent.ConfiguredTool {
	return append([]agent.ConfiguredTool(nil), c.Tools...)
}

func (c managedAgentRuntimeConfig) networkSettings() managedAgentNetworkConfig {
	return managedAgentNetworkConfig{
		ReachableHosts: append([]managedAgentHostMatcher(nil), c.Network.ReachableHosts...),
	}
}

func (c managedAgentRuntimeConfig) httpHeaderSettings() []managedAgentHTTPHeader {
	return append([]managedAgentHTTPHeader(nil), c.HTTP.Headers...)
}

func (c managedAgentRuntimeConfig) outputFileThresholdBytes() int {
	return c.OutputFileThresholdBytes
}

func loadManagedAgentRuntimeConfig(configJSON string) (managedAgentRuntimeConfig, error) {
	if strings.TrimSpace(configJSON) == "" {
		return managedAgentRuntimeConfig{}, nil
	}
	var config managedAgentRuntimeConfig
	if err := cpstore.DecodeEnvelopeInto(configJSON, &config); err != nil {
		return managedAgentRuntimeConfig{}, err
	}
	return config, nil
}

func loadAgentMemorySettings(configJSON string) (AgentMemorySpec, error) {
	config, err := loadManagedAgentRuntimeConfig(configJSON)
	if err != nil {
		return AgentMemorySpec{}, err
	}
	return config.memorySettings(), nil
}

func loadAgentFilesystemSettings(configJSON string) (managedAgentFilesystemConfig, error) {
	config, err := loadManagedAgentRuntimeConfig(configJSON)
	if err != nil {
		return managedAgentFilesystemConfig{}, err
	}
	return config.filesystemSettings(), nil
}

func loadAgentMountSettings(configJSON string) ([]managedAgentMount, error) {
	config, err := loadManagedAgentRuntimeConfig(configJSON)
	if err != nil {
		return nil, err
	}
	return config.mountSettings(), nil
}

func loadAgentCapabilities(configJSON string) (map[string]any, error) {
	config, err := loadManagedAgentRuntimeConfig(configJSON)
	if err != nil {
		return nil, err
	}
	return config.capabilities(), nil
}

func loadAgentToolSettings(configJSON string) ([]agent.ConfiguredTool, error) {
	config, err := loadManagedAgentRuntimeConfig(configJSON)
	if err != nil {
		return nil, err
	}
	return config.toolSettings(), nil
}

func loadAgentNetworkSettings(configJSON string) (managedAgentNetworkConfig, error) {
	config, err := loadManagedAgentRuntimeConfig(configJSON)
	if err != nil {
		return managedAgentNetworkConfig{}, err
	}
	return config.networkSettings(), nil
}

func loadAgentHTTPHeaderSettings(configJSON string) ([]managedAgentHTTPHeader, error) {
	config, err := loadManagedAgentRuntimeConfig(configJSON)
	if err != nil {
		return nil, err
	}
	return config.httpHeaderSettings(), nil
}

func composeManagedSystemPrompt(record cpstore.RunnableAgent, capabilities map[string]any, memory AgentMemorySpec, mounts []managedAgentMount, network managedAgentNetworkConfig, httpHeaders []managedAgentHTTPHeader) string {
	customPrompt := strings.TrimSpace(record.SystemPrompt)
	if capabilityBool(capabilities, capabilityAllowMessageSend) {
		customPrompt = appendManagedPromptSection(customPrompt, "Agent-to-agent messaging", mailboxPromptGuidance())
	}
	customPrompt = appendManagedPromptSection(customPrompt, "Mounted resources", mountPromptGuidance(mounts))
	customPrompt = appendManagedPromptSection(customPrompt, "Large tool outputs", largeToolOutputPromptGuidance())
	if record.AllowNetwork {
		customPrompt = appendManagedPromptSection(customPrompt, "Network policy", networkPromptGuidance(network))
		customPrompt = appendManagedPromptSection(customPrompt, "Automatic HTTP headers", httpHeaderPromptGuidance(httpHeaders))
	}
	customPrompt = appendManagedPromptSection(customPrompt, "Persistent memory guidance", memoryPromptGuidance(memory))
	return agent.ComposeSystemPrompt(customPrompt, record.AllowNetwork)
}

func (c managedAgentFilesystemConfig) normalized() managedAgentFilesystemConfig {
	return managedAgentFilesystemConfig{Kind: c.kind()}
}

func (c managedAgentFilesystemConfig) kind() string {
	switch strings.TrimSpace(c.Kind) {
	case "", managedAgentFilesystemKindDisk:
		return managedAgentFilesystemKindDisk
	case managedAgentFilesystemKindMemory:
		return managedAgentFilesystemKindMemory
	default:
		return strings.TrimSpace(c.Kind)
	}
}

func appendManagedPromptSection(prompt, title, guidance string) string {
	guidance = strings.TrimSpace(guidance)
	if guidance == "" {
		return prompt
	}
	if strings.TrimSpace(prompt) == "" {
		return title + ":\n" + guidance
	}
	return prompt + "\n\n" + title + ":\n" + guidance
}

func mailboxPromptGuidance() string {
	return strings.TrimSpace(`When you need to send work or a follow-up to another managed agent in the same namespace, return an outbox envelope in your final "result" value:
{
  "reply": "optional summary of what you decided or completed",
  "outbox": [
    {
      "recipient_agent_id": "other-agent-id",
      "payload": { ... }
    }
  ]
}
Each outbox entry must include "recipient_agent_id" and "payload".
Optional outbox fields are "thread_id" to override the current thread, "kind" (defaults to "agent.message"), "metadata", "available_at" as an RFC3339 timestamp, and "max_attempts".
Cross-namespace delivery is not supported. The recipient agent must exist in your current namespace.
If you are not sending messages, you may finish with any normal result value.
Only the final "result" value from a "finish" response is inspected for outbound messages. Shell output does not enqueue messages.`)
}

func memoryPromptGuidance(memory AgentMemorySpec) string {
	if memory.Disable {
		return ""
	}
	if override := strings.TrimSpace(memory.PromptOverride); override != "" {
		return override
	}
	return strings.TrimSpace(fmt.Sprintf(`You have a persistent memory directory at %s inside your sandbox root.
If %s exists, read it at the start of each run before loading any deeper memory files.
If it does not exist yet, create it the first time you need durable memory.
Keep %s small. Use it as an index of URNs to more detailed files or directories.
Only load deeper memory files when they are relevant to the current task.
Reading too much memory at once can pollute your context, so prefer the smallest relevant file set.
Store durable facts, preferences, and ongoing work state in topic files or subdirectories instead of dumping everything into %s.
If you learn something interesting or useful that could help with subsequent runs, consider recording it in the relevant memory file.`,
		defaultAgentMemoryDir+"/",
		defaultAgentMemoryRootRelativePath(),
		defaultAgentMemoryRootFile,
		defaultAgentMemoryRootFile,
	))
}

func defaultAgentMemoryRootRelativePath() string {
	return defaultAgentMemoryDir + "/" + defaultAgentMemoryRootFile
}

func mountPromptGuidance(mounts []managedAgentMount) string {
	if len(mounts) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("The runtime placed the following mounts into your sandbox before this run started.\n")
	builder.WriteString("These are sandbox-local copies, not live links to their original sources.\n")
	builder.WriteString("If you edit them, you are editing the sandbox copy.\n")
	builder.WriteString("Use them when relevant:\n")
	for _, mount := range mounts {
		builder.WriteString("- ")
		builder.WriteString(filepath.ToSlash(mount.Path))
		builder.WriteString(" (")
		builder.WriteString(mount.kind())
		builder.WriteString(")")
		if description := strings.TrimSpace(mount.Description); description != "" {
			builder.WriteString(": ")
			builder.WriteString(description)
		}
		builder.WriteString("\n")
	}
	return strings.TrimSpace(builder.String())
}

func largeToolOutputPromptGuidance() string {
	return strings.TrimSpace(`Some large tool outputs may be written into sandbox temp files for the current run instead of being fully inlined.
When a step summary references one of those files, read it only if you need the full content.
These runtime spill files are temporary and are separate from pre-run mounts.`)
}
