# GitHub Repo Inspector

This example shows an agent that configures `network.reachable_hosts` and talks to the GitHub REST API through the built-in `http_request` tool surface.

It adds:

- `network.reachable_hosts` scoped to `api.github.com`
- managed `http.headers` rules scoped to `api.github.com`
- a sandbox directory where the agent writes a summary file
- a prompt that explicitly prefers `http_request` over ad hoc shell networking

No extra API token is required for the public repository metadata this example fetches.

## Validate

```sh
go run ./pkg/server/cmd/swarmd config validate \
  --config-root ./examples/agents/github-repo-inspector/server-config
```

## Run

```sh
go run ./pkg/server/cmd/swarmd server \
  --config-root ./examples/agents/github-repo-inspector/server-config \
  --data-dir ./.tmp/swarmd/github-repo-inspector
```

After the schedule fires, check:

- `examples/agents/github-repo-inspector/sandbox/reports/latest-summary.md`
