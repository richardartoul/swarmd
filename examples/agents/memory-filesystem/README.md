# Memory Filesystem

This example shows a managed agent running on the in-memory filesystem backend.

It adds:

- `runtime.filesystem.kind: memory`
- `root_path: /workspace` as the logical shell-visible root
- `preserve_state: true` so warm worker reuse can keep virtual files alive
- a simple warm-state file under `/workspace/demo/history.txt`
- one `server_log` call per trigger describing whether prior warm state was present

## Validate

```sh
go run ./pkg/server/cmd/swarmd config validate \
  --config-root ./examples/agents/memory-filesystem/server-config
```

## Run

```sh
go run ./pkg/server/cmd/swarmd server \
  --config-root ./examples/agents/memory-filesystem/server-config \
  --data-dir ./.tmp/swarmd/memory-filesystem
```

## Inspect

Confirm that the synced agent is marked as memory-backed:

```sh
go run ./pkg/server/cmd/swarmd agent show \
  --db ./.tmp/swarmd/memory-filesystem/swarmd-server.db \
  --namespace demo \
  --agent memory-filesystem
```

The `root` field should render as `/workspace (memory)`.

The agent keeps a small history file inside the virtual filesystem while the same
worker instance stays warm. When the schedule fires repeatedly without recreating
the worker, later runs should report that prior warm state existed. Restarting
the runtime recreates the in-memory filesystem from scratch, so that state is
reset.
