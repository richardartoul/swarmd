# GitHub Monorepo Assistant

This example shows a monorepo-oriented agent that uses the stock `github_read_repo`, `github_read_reviews`, and `github_read_ci` custom tools together.

It adds:

- `tools:` entries for the three GitHub read tools
- tool-scoped GitHub host access declared by the tools themselves
- a sandbox directory where the agent writes a Markdown summary
- a prompt that combines repository discovery, review context, and CI inspection in one flow

The GitHub tools now carry their own scoped host policies. Metadata, reviews, checks, and `github_read_ci` log or artifact downloads all use tool-owned allowlists, including the extra GitHub redirect and storage hosts that CI downloads need. Add `network.reachable_hosts` only if you also want shell access or the global network tools.

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
