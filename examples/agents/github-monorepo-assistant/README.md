# GitHub Monorepo Assistant

This example shows a monorepo-oriented agent that uses the stock `github_read_repo`, `github_read_reviews`, and `github_read_ci` custom tools together.

It adds:

- `tools:` entries for the three GitHub read tools
- `network.reachable_hosts` scoped to `api.github.com`
- a sandbox directory where the agent writes a Markdown summary
- a prompt that combines repository discovery, review context, and CI inspection in one flow

The sample network policy is intentionally narrow. Metadata, reviews, checks, and workflow listings work against `api.github.com`, but `github_read_ci` log and artifact downloads can follow redirects to GitHub storage hosts and need a broader allowlist before those actions will succeed.

This example requires both `OPENAI_API_KEY` and `GITHUB_TOKEN`, even though the target repository can be public, because the GitHub custom tools are server-owned and token-backed.

## Validate

```sh
go run ./pkg/server/cmd/swarmd config validate \
  --config-root ./examples/agents/github-monorepo-assistant/server-config
```

## Run

```sh
go run ./pkg/server/cmd/swarmd server \
  --config-root ./examples/agents/github-monorepo-assistant/server-config \
  --data-dir ./.tmp/swarmd/github-monorepo-assistant
```

## Inspect

After the schedule fires, check:

- `examples/agents/github-monorepo-assistant/sandbox/reports/latest-summary.md`
