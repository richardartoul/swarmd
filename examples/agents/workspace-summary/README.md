# Workspace Summary

This example shows how to point an agent at a real workspace and give it extra context through mounts.

It adds:

- `root_path` set to a sample workspace inside this example directory
- a mounted markdown template loaded from disk
- a mounted inline context file
- `preserve_state: true`
- a scheduled report written into the demo workspace itself

## Validate

```sh
go run ./pkg/server/cmd/swarmd config validate \
  --config-root ./examples/agents/workspace-summary/server-config
```

## Run

```sh
go run ./pkg/server/cmd/swarmd server \
  --config-root ./examples/agents/workspace-summary/server-config \
  --data-dir ./.tmp/swarmd/workspace-summary
```

After the schedule fires, check:

- `examples/agents/workspace-summary/sample-workspace/reports/latest-summary.md`

That file is the agent-written summary for the sample workspace in this example.
