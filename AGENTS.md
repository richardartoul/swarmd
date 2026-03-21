# AGENTS.md

## Cursor Cloud specific instructions

### Overview

swarmd is a pure Go project (Go 1.25.0) — a runtime for sandboxed, YAML-defined AI agents. SQLite is embedded (pure-Go, no external DB). No Docker, Node.js, or Python required.

### Build, Lint, Test

Standard Go commands from the repo root:

- **Build:** `go build ./...`
- **Lint:** `go vet ./...`
- **Test:** `go test ./...`
  - Tests use scripted/mock drivers; no API keys needed.
  - A few tests in `pkg/sh/interp` may fail in non-standard environments (they compare against host `bash` output). These are pre-existing and environment-sensitive.

### Running the server

See `README.md` "Quick Start" section. The server requires an LLM API key (`OPENAI_API_KEY` or `ANTHROPIC_API_KEY`) to process agent tasks but starts and syncs config without one. Example:

```sh
go run ./pkg/server/cmd/swarmd server \
  --config-root ./examples/agents/hello-heartbeat/server-config \
  --data-dir ./.tmp/swarmd/hello-heartbeat
```

### Running without API keys

The embedding examples under `examples/embedding/` use scripted drivers and run without any API keys:

```sh
go run ./examples/embedding/minimal-agent
go run ./examples/embedding/custom-tool
go run ./examples/embedding/network-policy
```

### Makefile targets

The `makefile` has four convenience targets: `agentrepl`, `agentrepl-memory`, `server`, `tui`. These use `go run` and are documented in the makefile itself.

### Key gotchas

- The `go.mod` specifies `go 1.25.0`. Ensure the Go toolchain matches.
- The `.tmp/` directory is used for ephemeral server data (SQLite DB, agent sandboxes). It is gitignored.
