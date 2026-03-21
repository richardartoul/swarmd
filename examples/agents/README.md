# Agent Examples

Each directory under this folder contains:

- a short explainer
- a self-contained `server-config` tree
- any supporting files needed by that example

Use the `server-config` subdirectory as `--config-root`.
All examples here use the namespace id `demo`.

Most agent examples require `OPENAI_API_KEY` because the stock `swarmd` binary uses the OpenAI-backed worker driver.

## Common Commands

Validate an example:

```sh
go run ./pkg/server/cmd/swarmd config validate \
  --config-root ./examples/agents/hello-heartbeat/server-config
```

Run an example in isolation:

```sh
go run ./pkg/server/cmd/swarmd server \
  --config-root ./examples/agents/hello-heartbeat/server-config \
  --data-dir ./.tmp/swarmd/hello-heartbeat
```

Inspect the synced agent:

```sh
go run ./pkg/server/cmd/swarmd agent show \
  --db ./.tmp/swarmd/hello-heartbeat/swarmd-server.db \
  --namespace demo \
  --agent hello-heartbeat
```

## Included Examples

### [hello-heartbeat](hello-heartbeat/README.md)

The smallest stock server example. It shows one scheduled agent, one stock custom tool (`server_log`), and no network access.

### [workspace-summary](workspace-summary/README.md)

A self-contained filesystem example. It points `root_path` at a sample workspace, mounts a reusable template and inline context, preserves state between runs, and writes a report into the demo workspace.

### [memory-filesystem](memory-filesystem/README.md)

A managed in-memory filesystem example. It sets `runtime.filesystem.kind: memory`, uses `/workspace` as a logical root, preserves warm virtual state between runs, and logs whether prior in-memory state was present.

### [github-repo-inspector](github-repo-inspector/README.md)

An example that configures `network.reachable_hosts` and uses the built-in `http_request` tool surface plus managed `http.headers` rules to talk to the GitHub API and write a summary into a sandbox directory.

### [github-monorepo-assistant](github-monorepo-assistant/README.md)

A GitHub custom-tool example that combines `github_read_repo`, `github_read_reviews`, and `github_read_ci` to inspect one shared repository and write a summary into a sandbox directory.
