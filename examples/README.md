# Examples

This directory is the canonical map for runnable examples in the repository.
Use these examples when you want isolated demos or minimal embedding samples.

The repository also ships a default `../server-config/` tree that is not part of this folder. That root is what `go run ./pkg/server/cmd/swarmd server`, `go run ./pkg/server/cmd/swarmd tui`, `make server`, and `make tui` use when you do not pass `--config-root`.

## Quick Reference

| Path | Kind | Use it when... |
| --- | --- | --- |
| [agents](agents/README.md) | server examples | you want self-contained `server-config` trees for the `swarmd` binary and CLI |
| [embedding](embedding/README.md) | embedding examples | you want small Go programs that use `pkg/agent` directly |
| `../server-config/` | repository default config root | you want the stock configs used by the default `swarmd server` and `swarmd tui` flows |

## How To Use These Examples

Agent examples are organized so each example directory contains its own `server-config` root. Pass that directory directly to the `config validate` or `server` subcommand of the `swarmd` CLI.

That collection now includes both disk-backed and in-memory filesystem demos,
including [agents/memory-filesystem](agents/memory-filesystem/README.md) and a GitHub custom-tool workflow in [agents/github-monorepo-assistant](agents/github-monorepo-assistant/README.md).

Most checked-in agent examples require `OPENAI_API_KEY` because their configs use the OpenAI-backed worker driver by default. Anthropic-backed configs should use `model.provider: anthropic` and provide `ANTHROPIC_API_KEY`.

Validate an example:

```sh
go run ./pkg/server/cmd/swarmd config validate \
  --config-root ./examples/agents/hello-heartbeat/server-config
```

Run an example in isolation so it does not mix with any other local state:

```sh
go run ./pkg/server/cmd/swarmd server \
  --config-root ./examples/agents/workspace-summary/server-config \
  --data-dir ./.tmp/swarmd/workspace-summary
```

Embedding examples are ordinary Go programs. The scripted ones do not require `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, or a running server. The provider-backed ones require the matching API key:

```sh
# scripted examples
go run ./examples/embedding/minimal-agent
go run ./examples/embedding/custom-tool
go run ./examples/embedding/network-policy

# provider-backed examples
OPENAI_API_KEY=... go run ./examples/embedding/minimal-agent-openai
ANTHROPIC_API_KEY=... go run ./examples/embedding/minimal-agent-anthropic
```

## Directory Map

- [agents](agents/README.md): self-contained `server-config` trees for the `swarmd` binary and CLI
- [embedding](embedding/README.md): standalone Go programs that use `pkg/agent` directly
