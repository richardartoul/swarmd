# Embedding Examples

These examples use `pkg/agent` directly instead of running the `swarmd` binary.
The scripted examples run without provider credentials. The provider-backed examples call remote model APIs and require the matching API key.

Run them from the repository root:

```sh
# scripted examples
go run ./examples/embedding/minimal-agent
go run ./examples/embedding/custom-tool
go run ./examples/embedding/network-policy

# provider-backed examples
OPENAI_API_KEY=... go run ./examples/embedding/minimal-agent-openai
ANTHROPIC_API_KEY=... go run ./examples/embedding/minimal-agent-anthropic
```

## Included Examples

### `minimal-agent`

The smallest possible scripted `pkg/agent` program. It creates an agent with a temporary sandbox, runs one shell step, and prints the resulting output.

### `minimal-agent-openai`

The smallest OpenAI-backed `pkg/agent` program. It requires `OPENAI_API_KEY` and uses `gpt-4o-mini` by default.

### `minimal-agent-anthropic`

The smallest Anthropic-backed `pkg/agent` program. It requires `ANTHROPIC_API_KEY` and uses `claude-sonnet-4-6` by default.

### `custom-tool`

Registers a small structured tool, enables it through `ConfiguredTools`, passes tool-specific config, and injects host-owned runtime data through `ToolRuntimeData`.

### `network-policy`

Shows three runs side by side:

- one without networking, where `curl` is hidden from the sandbox surface
- one with a host allowlist that blocks the target outright
- one with a host allowlist that permits a local request and injects an HTTP header into it
