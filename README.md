# swarmd

`swarmd` sits somewhere in the awkward intersection between "OpenClaw for Enterprise" and "Kubernetes for Agents".

**WARNING**: `swarmd` is alpha software. It has not yet been extensively tested at scale or hardened for production environments.

- [Overview](#overview)
- [Quick Start](#quick-start)
- [Deployment](#deployment)
- [Examples](#examples)
- [Agent YAML](#agent-yaml)
- [Custom Tool Catalog](#custom-tool-catalog)
- [Adding Custom Tools](#adding-custom-tools)
- [Acknowledgements](#acknowledgements)
- [Motivation](#motivation)

## Overview

`swarmd` is a multi-tenant runtime for running background Agents in a safe and secure manner. Agents are defined in YAML and run as goroutines in a multi-tenant server with a virtual shell and custom tools. `swarmd` is not a generic sandbox for running existing agent harnesses. It is a stand-alone agent harness designed from the ground up with sandboxing in mind.

```yaml
version: 1
agent_id: hello-heartbeat
name: Hello Heartbeat
model:
  name: gpt-5
prompt: |
  Use `server_log` to write exactly one info log entry, then finish with a short confirmation.
tools:
  - server_log
schedules:
  - id: every-minute
    cron: "* * * * *"
    timezone: UTC
```

`pkg/agent` can also be used directly from Go applications to embed sandboxed agents inside your own application without the full `swarmd` server. See [examples/embedding](examples/embedding/README.md) for small end-to-end embedding examples.

`swarmd` does not rely on any operating system sandboxing primitives. It will run anywhere you can run a Go application, and it works exactly the same in all environments.

`swarmd` Agents have zero direct access to the host operating system: filesystem operations go through a filesystem interface that limits access to a specific subdirectory (or fake in-memory filesystem), and network access goes through a network interface with a custom dialer plus a managed HTTP layer for host-owned header injection.

All Agents have access to the same built-in tools:

- `apply_patch`: apply a structured patch to local files
- `describe_image`: describe an image through the active provider's native vision API using a sandbox file path, inline base64, or a public image URL
- `grep_files`: search local files with a regular expression and return matching paths
- `http_request`: make direct HTTP requests for API-style interactions
- `list_dir`: list entries in a local directory with bounded output
- `read_file`: read a local file with numbered, bounded output
- `read_web_page`: fetch a web page and convert it to markdown or text
- `run_shell`: run one sandboxed shell command when no structured tool fits
- `web_search`: search the public web through the runtime-owned search backend

Additional custom tools can also be compiled into the server, but Agents only receive them on an allow-list basis: the tool must be present in the server binary and explicitly listed under the Agent's `tools:` field. See [Custom Tool Catalog](#custom-tool-catalog) for the currently available custom tools, or [Adding Custom Tools](#adding-custom-tools) to write your own.

Agent activity and state is tracked in a local SQLite database that can be investigated with a local TUI.

![TUI screenshot 1](docs/tui_screenshot_1.png)

![TUI screenshot 2](docs/tui_screenshot_2.png)

![TUI screenshot 3](docs/tui_screenshot_3.png)

The runtime also includes a persistent memory system inside the sandboxed filesystem. Agents can keep durable notes under `.memory/`, use `.memory/ROOT.md` as a small index, and load deeper topic files only when they are relevant to the current task.

See [Agent YAML](#agent-yaml) for the short version and [docs/agent-yaml-guide.md](docs/agent-yaml-guide.md) for the full reference.

## Quick Start

`swarmd` expects a config root with a nested directory layout of YAML agent specs under `namespaces/<namespace>/agents/*.yaml`. There are two easy ways to get started: install the `swarmd` binary and scaffold that directory structure locally, or clone this repository and run one of the bundled examples.

The stock `swarmd` binary supports both OpenAI and Anthropic worker drivers. The bundled example configs still default to OpenAI, so the commands below use `OPENAI_API_KEY`. Anthropic-backed configs should set `model.provider: anthropic` and provide `ANTHROPIC_API_KEY`.

When provider-native reasoning is available, the REPL/TUI surfaces it as per-step thinking before tool calls. OpenAI reasoning summaries are requested automatically on supported reasoning models, while Anthropic summarized thinking appears when the API returns visible `thinking` blocks. Final-response `thought` metadata is best-effort and no longer required for a successful finish.

### Install The Binary

If you want a local config root to start from, install `swarmd` directly and let `swarmd init` create the default directory structure plus a sample heartbeat agent:

```sh
go get github.com/richardartoul/swarmd/pkg/server/cmd/swarmd@latest
export OPENAI_API_KEY=your-openai-api-key
swarmd init
swarmd config validate
swarmd server
```

That bootstraps `./server-config/namespaces/default/agents/server-log-heartbeat.yaml` and stores server state under `./data/`. In another terminal, open the TUI against that local SQLite database:

```sh
swarmd tui
```

### Clone The Repository And Run An Example

If you prefer to start from a checked-in example, clone the repository and point `swarmd` at one of the example config roots:

```sh
git clone https://github.com/richardartoul/swarmd.git
cd swarmd
export OPENAI_API_KEY=your-openai-api-key
go run ./pkg/server/cmd/swarmd server \
  --config-root ./examples/agents/hello-heartbeat/server-config \
  --data-dir ./.tmp/swarmd/hello-heartbeat
```

Open the TUI against the example database:

```sh
go run ./pkg/server/cmd/swarmd tui \
  --db ./.tmp/swarmd/hello-heartbeat/swarmd-server.db
```

For the full runnable walkthrough, start with [examples/agents/hello-heartbeat](examples/agents/hello-heartbeat/README.md) or browse [examples/README.md](examples/README.md) for more example roots.

## Deployment

`swarmd` is a simple Go binary, so you can deploy it however you want. The easiest place to start is usually a decent-sized virtual machine with the binary, your agent YAML config root, and a persistent disk for the data directory.

The primary database is SQLite, so backups are usually just backups of that database file. In general, agent YAMLs should live in version control, while SQLite is mostly tracking execution history and runtime state. A persistent disk is only required if you want to preserve an agent's filesystem contents or other sandbox state between runs.

See [Adding Custom Tools](#adding-custom-tools) for instructions on deploying tools that are specific to your environment.

## Examples

- [examples/agents/hello-heartbeat](examples/agents/hello-heartbeat/README.md): the smallest scheduled server example using the stock `server_log` tool
- [examples/agents/memory-filesystem](examples/agents/memory-filesystem/README.md): a managed in-memory filesystem example using `runtime.filesystem.kind: memory` with warm state preserved while the same worker stays alive
- [examples/agents/workspace-summary](examples/agents/workspace-summary/README.md): a filesystem-heavy example that mounts reusable context and writes a report into a demo workspace
- [examples/agents/github-repo-inspector](examples/agents/github-repo-inspector/README.md): a networked example that configures `network.reachable_hosts` and managed `http.headers`
- [examples/agents/github-monorepo-assistant](examples/agents/github-monorepo-assistant/README.md): a GitHub custom-tool example that combines repository, review, and CI reads for one shared repo
- [examples/embedding](examples/embedding/README.md): small Go programs that use `pkg/agent` directly without running the full server

## Agent YAML

The root README keeps the short version. The full reference lives in [docs/agent-yaml-guide.md](docs/agent-yaml-guide.md).

Filesystem-managed agent specs live under:

```text
server-config/
  namespaces/
    <namespace>/
      agents/
        <agent>.yaml
```

A minimal agent spec looks like this:

```yaml
version: 1
model:
  name: gpt-5
prompt: |
  List the files in the current workspace and summarize what you find.
root_path: .
```

A slightly fuller spec can allow-list a custom tool, open outbound access to a specific host, and inject a server-owned HTTP credential from an environment variable:

```yaml
version: 1
model:
  name: gpt-5
prompt: |
  Inspect the repository and query the internal status API.
root_path: .
tools:
  - github_read_repo
network:
  reachable_hosts:
    - glob: api.internal.example.com
http:
  headers:
    - name: Authorization
      env_var: INTERNAL_STATUS_API_TOKEN
      domains:
        - glob: api.internal.example.com
```

In this example, `github_read_repo` is a custom tool explicitly allow-listed under `tools:`. `network.reachable_hosts` allows shell and global network tools to reach `api.internal.example.com`, and `http.headers[].env_var` injects a server-owned credential for that host without storing the secret in the prompt or workspace. Built-in tools should not be listed under `tools:`. See [Custom Tool Catalog](#custom-tool-catalog) for the stock custom tools and [docs/agent-yaml-guide.md](docs/agent-yaml-guide.md) for the full YAML reference.

The full guide covers:

- config root layout and path rules
- memory guidance, including the default `.memory/ROOT.md` workflow
- sandbox filesystem and mounts
- network policy and managed HTTP headers
- built-in vs custom structured tools
- runtime tuning, schedules, validation rules, and environment variables

## Custom Tool Catalog

The stock tool surface has two parts: built-in tools that every Agent gets automatically, and additional custom tools that Agents only receive when allow-listed under `tools:`.

All Agents always get these built-in tools, and they should not be listed under `tools:`:

- `apply_patch`: apply a structured patch to local files
- `describe_image`: describe an image through the active provider's native vision API using a sandbox file path, inline base64, or a public image URL
- `grep_files`: search local files with a regular expression and return matching paths
- `http_request`: make direct HTTP requests for API-style interactions
- `list_dir`: list entries in a local directory with bounded output
- `read_file`: read a local file with numbered, bounded output
- `read_web_page`: fetch a web page and convert it to markdown or text
- `run_shell`: run one sandboxed shell command when no structured tool fits
- `web_search`: search the public web through the runtime-owned search backend

The stock `swarmd` server binary in this repo also includes these additional allow-list tools. Agents only get them when they are explicitly listed under `tools:`:

- `datadog_read`: read incidents, monitors, dashboards, metrics, logs, and events from Datadog
- `github_read_ci`: read GitHub statuses, checks, workflows, jobs, logs, and artifacts
- `github_read_repo`: read GitHub repository metadata, trees, files, branches, and rulesets
- `github_read_reviews`: read GitHub issues, pull requests, reviews, comments, commits, and timelines
- `server_log`: write a message to the server logs with namespace and agent context
- `slack_dm`: send a Slack direct message to one user
- `slack_channel_history`: list Slack channel timeline messages newer than a timestamp
- `slack_post`: post a Slack message or thread reply
- `slack_replies`: list replies for a Slack thread

If you want to add another tool to your own deployment, see [Adding Custom Tools](#adding-custom-tools).

## Adding Custom Tools

`swarmd` is designed to make it straightforward to add new tools. If the tool you're adding is generic and would be widely applicable to many users, consider making a pull request to the primary repo. If the tool is specific to your environment / workflow, then fork `swarmd` and follow the instructions below for adding a new custom tools package:

1. Create a new package under `pkg/tools/<name>`.
2. Implement a `toolscore.ToolPlugin` with `Definition()` and `NewHandler()`.
3. Register it from `init()` with `server.RegisterTool(...)`.
4. Add a blank import in `pkg/tools/customtools/customtools.go`.
5. Reference the tool id from agent YAML under `tools:`.

For the stock `swarmd` server binary, new custom tools should follow the same pattern as `pkg/tools/serverlog`, `pkg/tools/slackpost`, `pkg/tools/datadogread`, and the GitHub read tool packages under `pkg/tools/githubreadrepo`, `pkg/tools/githubreadreviews`, and `pkg/tools/githubreadci`: self-register on import, then get pulled into the binary through `pkg/tools/customtools`.

A minimal server-backed tool looks like this:

```go
package hellotool

import (
	"context"
	"strings"
	"sync"

	"github.com/richardartoul/swarmd/pkg/server"
	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
)

const toolName = "hello_tool"

var registerOnce sync.Once

type plugin struct{}

type input struct {
	Name string `json:"name"`
}

func init() {
	Register()
}

func Register() {
	registerOnce.Do(func() {
		server.RegisterTool(func(_ server.ToolHost) toolscore.ToolPlugin {
			return plugin{}
		}, server.ToolRegistrationOptions{})
	})
}

func (plugin) Definition() toolscore.ToolDefinition {
	return toolscore.ToolDefinition{
		Name:        toolName,
		Description: "Return a short greeting.",
		Kind:        toolscore.ToolKindFunction,
		Parameters: toolscommon.ObjectSchema(
			map[string]any{
				"name": toolscommon.StringSchema("Name to greet."),
			},
			"name",
		),
		RequiredArguments: []string{"name"},
		ReadOnly:          true,
	}
}

func (plugin) NewHandler(cfg toolscore.ConfiguredTool) (toolscore.ToolHandler, error) {
	if err := toolscommon.ValidateNoToolConfig(toolName, cfg.Config); err != nil {
		return nil, err
	}
	return toolscore.ToolHandlerFunc(func(_ context.Context, toolCtx toolscore.ToolContext, step *toolscore.Step, call *toolscore.ToolAction) error {
		input, err := toolscore.DecodeToolInput[input](call.Input)
		if err != nil {
			toolCtx.SetPolicyError(step, err)
			return nil
		}
		output, err := toolscommon.MarshalToolOutput(map[string]any{
			"message": "hello, " + strings.TrimSpace(input.Name),
		})
		if err != nil {
			return err
		}
		toolCtx.SetOutput(step, output)
		return nil
	}), nil
}
```

Then import it so the server binary includes it:

```go
package customtools

import (
	_ "github.com/richardartoul/swarmd/pkg/tools/hellotool"
)
```

And enable it in agent YAML:

```yaml
tools:
  - id: hello_tool
```

If the tool needs per-agent configuration, pass it under `tools[].config`; that map is provided to `NewHandler(cfg toolscore.ConfiguredTool)`.

If the tool needs outbound HTTP, pick the right `NetworkScope` in `Definition()`:

- use `toolscore.ToolNetworkScopeGlobal` when it should share the agent's `network.reachable_hosts` policy
- use `toolscore.ToolNetworkScopeScoped` plus `server.ToolRegistrationOptions{RequiredHosts: ...}` when it should auto-allow a fixed host set only for that tool

Scoped tool hosts do not get added to shell access or other tools.

If the tool needs host environment variables such as API keys, declare them in `server.ToolRegistrationOptions{RequiredEnv: ...}` so validation fails early when the binary is misconfigured.

Two rules are easy to miss:

- built-in tools should not be listed under `tools`; that YAML field is only for custom tools compiled into your fork
- tool ids are registry names such as `hello_tool`, not shell commands or Go package paths

After adding a tool, validate the config and run the server from the repo root:

```sh
go run ./pkg/server/cmd/swarmd config validate
go run ./pkg/server/cmd/swarmd server
```

If you are embedding `pkg/agent` instead of forking the `swarmd` binary, see `examples/embedding/custom-tool` for the same `Definition()`/`NewHandler()` flow using `agent.RegisterTool(...)`.

## Acknowledgements

The virtual shell in this repository is a heavily forked and modified version of [`mvdan/sh`](https://github.com/mvdan/sh).

## Motivation

I created `swarmd` because I wanted to automate tasks using agents at work, but in a safe and secure manner that did not involve months of security reviews or deep operating-system sandbox expertise just to get something deployed. There is a huge gap between what models are capable of doing *in principle* and what companies are actually able to deploy them to do *in practice*.

For example, consider a very basic automation task: have an LLM automatically monitor your observability system to detect new deployments, and when it detects one, analyze the rollout to look for anomalies, correlate them with code, and post a message with the findings in Slack. Modern LLMs are fully capable of doing this. You could write a 200-word prompt right now, give it to Cursor, Claude Code, or Codex running on your laptop, connect a few MCP servers, and it would work.

But most companies are not doing things like this yet. Why? Because they cannot figure out how to deploy these workloads in a sane manner. Most companies want automation like this to be centralized, not running on developer laptops. But how do you deploy a workflow like this "to production"? You could throw Claude Code on an EC2 instance in your production environment and use its built-in sandboxing, or lock it up in a container, but do you trust that? If the model does do something it was not supposed to do because of a sandbox gap, do you have logs?

`swarmd` is an attempt to answer some of those questions with a custom harness explicitly designed for deploying custom agents safely to production at actual enterprise companies.
