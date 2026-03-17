# Hello Heartbeat

This is the smallest stock server example in the repository.

It shows:

- one YAML-defined agent
- one stock custom tool, `server_log`
- one cron schedule
- no network access

## Validate

```sh
go run ./pkg/server/cmd/swarmd config validate \
  --config-root ./examples/agents/hello-heartbeat/server-config
```

## Run

```sh
go run ./pkg/server/cmd/swarmd server \
  --config-root ./examples/agents/hello-heartbeat/server-config \
  --data-dir ./.tmp/swarmd/hello-heartbeat
```

## Inspect

```sh
go run ./pkg/server/cmd/swarmd agent show \
  --db ./.tmp/swarmd/hello-heartbeat/swarmd-server.db \
  --namespace demo \
  --agent hello-heartbeat
```

When the schedule fires, the server should emit a log line tagged with the namespace and agent context.
