# Embedding Examples

These examples use `pkg/agent` directly instead of running the `swarmd` binary.
They all use scripted drivers, so they compile and run without `OPENAI_API_KEY`.

Run them from the repository root:

```sh
go run ./examples/embedding/minimal-agent
go run ./examples/embedding/custom-tool
go run ./examples/embedding/network-policy
```

## Included Examples

### `minimal-agent`

The smallest possible `pkg/agent` program. It creates an agent with a temporary sandbox, runs one shell step, and prints the resulting output.

### `custom-tool`

Registers a small structured tool, enables it through `ConfiguredTools`, passes tool-specific config, and injects host-owned runtime data through `ToolRuntimeData`.

### `network-policy`

Shows three runs side by side:

- one without networking, where `curl` is hidden from the sandbox surface
- one with a host allowlist that blocks the target outright
- one with a host allowlist that permits a local request and injects an HTTP header into it
