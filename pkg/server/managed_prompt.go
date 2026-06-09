// managed_prompt.go composes the managed worker system prompt from the
// agent's configured prompt plus capability-driven guidance sections.

package server

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/richardartoul/swarmd/pkg/agent"
	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

func composeManagedSystemPrompt(record cpstore.RunnableAgent, capabilities map[string]any, memory AgentMemorySpec, mounts []managedAgentMount, network managedAgentNetworkConfig, httpHeaders []managedAgentHTTPHeader) string {
	customPrompt := strings.TrimSpace(record.SystemPrompt)
	if capabilityBool(capabilities, capabilityAllowMessageSend) {
		customPrompt = appendManagedPromptSection(customPrompt, "Agent-to-agent messaging", mailboxPromptGuidance())
	}
	customPrompt = appendManagedPromptSection(customPrompt, "Mounted resources", mountPromptGuidance(mounts))
	customPrompt = appendManagedPromptSection(customPrompt, "Large tool outputs", largeToolOutputPromptGuidance())
	if len(network.ReachableHosts) > 0 {
		customPrompt = appendManagedPromptSection(customPrompt, "Network policy", networkPromptGuidance(network))
		customPrompt = appendManagedPromptSection(customPrompt, "Automatic HTTP headers", httpHeaderPromptGuidance(httpHeaders))
	}
	customPrompt = appendManagedPromptSection(customPrompt, "Persistent memory guidance", memoryPromptGuidance(memory))
	return agent.ComposeSystemPrompt(customPrompt)
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
