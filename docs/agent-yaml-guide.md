# Agent YAML Guide

This is the full reference for the YAML format used by the `swarmd` binary.
If you want onboarding and runnable first steps, start with [the root README](../README.md).
If you want concrete examples to copy, start with [examples/README.md](../examples/README.md) and [examples/agents/README.md](../examples/agents/README.md).

## Config Root Layout

Agent specs are loaded from:

```text
server-config/
  namespaces/
    <namespace>/
      agents/
        <agent>.yaml
```

Agent YAMLs are designed to live under one centralized config root. Each directory under `namespaces/` represents a namespace such as a team, organization, or environment, and that namespace's `agents/` directory contains the agent YAMLs for that namespace.

Example:

```text
server-config/
  namespaces/
    platform/
      agents/
        github-repo-inspector.yaml
        inbox-router.yaml
    payments/
      agents/
        refund-auditor.yaml
        incident-summary.yaml
```

Both `.yaml` and `.yml` files are accepted.

The path is part of the schema:

- `<namespace>` becomes the agent's `namespace_id`
- the file name stem becomes `agent_id` if `agent_id` is omitted inside the YAML
- unknown YAML fields are rejected

Path configuration:

- `--config-root` changes where the server looks for `namespaces/<namespace>/agents/*.yaml`
- `--root-base` changes the default fallback parent directory for sandbox roots when `root_path` is omitted
- `root_path` overrides the sandbox root for one agent, or the logical shell-visible root when `runtime.filesystem.kind: memory`
- `runtime.filesystem.kind` selects the default disk-backed sandbox or the in-memory backend
- `mounts[].path` controls where mounted files or directories appear inside the sandbox root
- only the config root itself is configurable today; the layout under that root is still fixed to `namespaces/<namespace>/agents/<file>.yaml`

## Minimal Example

```yaml
version: 1
model:
  name: gpt-5
prompt: |
  List the files in the current workspace and summarize what you find.
root_path: .
```

That is enough to load and sync an agent, although most agents also set `name`, `description`, runtime limits, and schedules.

## Full Example

```yaml
version: 1
agent_id: server-log-heartbeat
name: Server Log Heartbeat
description: Emits a heartbeat message to the server logs once a minute.
model:
  provider: openai
  name: gpt-5
prompt: |
  You are a server log heartbeat agent.

  When triggered, write exactly one log entry using the structured `server_log` tool.
  If the payload includes a string field named "level", use it. Otherwise use "info".
  If the payload includes a string field named "message", use it. Otherwise use "scheduled heartbeat from server-log-heartbeat".
root_path: /tmp/swarmd/default/server-log-heartbeat
tools:
  - server_log
runtime:
  desired_state: running
  preserve_state: false
  max_steps: 2
  step_timeout: 20s
  max_output_bytes: 16384
  retry_delay: 10s
  max_attempts: 3
schedules:
  - id: server-log-heartbeat-every-minute
    cron: "* * * * *"
    timezone: UTC
    enabled: true
    payload:
      level: info
      message: scheduled heartbeat from server-log-heartbeat
config:
  owner: sre
```

## Model And Prompt

These fields define what model the runtime uses and what instructions the agent receives.

| Field | Type | Notes |
| --- | --- | --- |
| `model.provider` | string | Defaults to `openai` when omitted. Allowed values are `openai` and `anthropic`. |
| `model.name` | string | Required model name, for example `gpt-5`. |
| `model.base_url` | string | Optional provider base URL override. |
| `prompt` | string | Required agent-specific instructions appended to the built-in system prompt. |
| `memory.disable` | bool | Disables the default persistent-memory guidance and `.memory/ROOT.md` workflow. |
| `memory.prompt_override` | string | Replaces the default persistent-memory guidance text. Cannot be set when `memory.disable` is `true`. |

Behavior notes:

- the YAML `prompt` is not the entire runtime prompt; the runtime prepends built-in shell-agent instructions, then appends your text under an agent-specific section
- `network.reachable_hosts` changes both runtime behavior and the built-in prompt; network-capable commands and tools only appear when that block is configured

## Sandboxed Filesystem

`root_path`, `runtime.filesystem.kind`, and `mounts` control what filesystem the agent can see.

| Field | Type | Notes |
| --- | --- | --- |
| `root_path` | string | Disk sandbox root, or the logical shell-visible root when `runtime.filesystem.kind` is `memory`. |
| `runtime.filesystem.kind` | string | `disk` or `memory`. Defaults to `disk`. |
| `mounts[].path` | string | Destination path inside the sandbox root. Must not resolve to the root itself or escape it. |
| `mounts[].description` | string | Optional human-readable description surfaced in prompt guidance. |
| `mounts[].source.path` | string | Host file or directory path. Relative paths resolve from the YAML file's directory. The runtime validates the path during sync, then snapshots it into the sandbox when a worker starts. |
| `mounts[].source.env_var` | string | Reads file contents from an environment variable value. |
| `mounts[].source.inline` | string | Inline file contents stored directly in YAML and embedded in the managed config. Keep this to small files. |

Mounts are sandbox-local copies, not live bind mounts. That stays true in both
disk and memory mode.
Exactly one of `mounts[].source.path`, `mounts[].source.env_var`, or `mounts[].source.inline` must be set for each mount.

`mounts[].source.path` behavior is intentionally deferred:

- sync validates that the current host source exists and is either a regular file or a directory tree of regular files
- the synced agent config stores path metadata, not the file bytes or per-file directory payloads
- worker startup re-reads the current host source and copies it into the sandbox
- changing host contents while a worker stays warm does not update the running sandbox copy or restart the worker
- updated host contents appear the next time the worker is recreated or restarted for some other reason
- top-level symlink sources are followed and canonicalized during sync, and directory trees still cannot contain nested symlinks or special files

`mounts[].source.inline` does not use deferred snapshots. Its contents stay embedded in config, so large inline blobs still increase config size.

`root_path` resolution works like this:

- in `runtime.filesystem.kind: disk`, if `root_path` is absolute, it is used as-is
- in `runtime.filesystem.kind: disk`, if `root_path` is relative, it is resolved relative to `--config-root`
- in `runtime.filesystem.kind: disk`, if `root_path` is omitted, the server falls back to `<data-dir>/agents/<namespace>/<agent_id>` by default
- in `runtime.filesystem.kind: disk`, if `root_path` is omitted and `--root-base` is set explicitly, the server falls back to `<root-base>/<namespace>/<agent_id>`
- in `runtime.filesystem.kind: disk`, if `root_path` is omitted and no root base can be resolved, sync fails
- in `runtime.filesystem.kind: memory`, `root_path` is a logical absolute path inside the virtual filesystem and defaults to `/` when omitted

Memory-mode lifecycle notes:

- the in-memory filesystem survives while the same live worker instance stays warm
- if the worker/runtime is recreated, the virtual filesystem is rebuilt from scratch and mounts are reapplied
- external host `exec.Cmd` bridging is not available in memory mode, so PATH files are only runnable when execution stays inside the interpreter's in-process command surface

Minimal memory-backed example:

```yaml
version: 1
model:
  name: gpt-5
prompt: |
  Keep short-lived scratch state under /workspace.
root_path: /workspace
runtime:
  filesystem:
    kind: memory
  preserve_state: true
```

For a runnable managed example, see [examples/agents/memory-filesystem](../examples/agents/memory-filesystem/README.md).

## Capabilities, Tools, And Network Policy

`capabilities`, `network`, `tools`, and `http` define what the agent is allowed to do.

Recognized capability fields:

| Field | Type | Default | Effect |
| --- | --- | --- | --- |
| `capabilities.allow_message_send` | bool | `false` | Allows a managed agent to enqueue same-namespace outbox messages by returning an `outbox` array in its final result envelope. |

Built-in structured tools such as `list_dir`, `read_file`, `grep_files`, `apply_patch`, and `run_shell` are always available by default, subject to capability gates such as networking.
When `network.reachable_hosts` is configured, built-in tools such as `http_request`, `read_web_page`, and `web_search` are also surfaced.

Outbound networking is configured through the top-level `network` block:

```yaml
network:
  reachable_hosts:
    - glob: "*.example.com"
    - regex: "api-[a-z0-9-]+\\.corp\\.internal"
```

Rules:

- omit `network` entirely to disable outbound networking
- set at least one `reachable_hosts` entry when `network` is present
- each entry must define exactly one of `glob` or `regex`
- matchers apply to the host only; protocol prefixes such as `https://` are rejected
- regex values may omit leading `^` and trailing `$`; the runtime anchors them internally
- use `glob: "*"` when you intentionally want unrestricted host reachability

The YAML `tools` field is only for explicit custom structured tools compiled into the current swarmd binary. Each entry may be either:

- a bare string id for a no-config custom tool
- an object with `id`, optional `config`, and optional `enabled`

The stock swarmd binary currently exposes these custom tools:

| Tool ID | Requires network | Tool config | Effect |
| --- | --- | --- | --- |
| `server_log` | No | none | Writes a message to the server logs with namespace and agent context attached automatically. |
| `slack_post` | Yes | `default_channel` | Posts a new Slack message or thread reply. |
| `slack_dm` | Yes | none | Sends a direct message to one Slack user by `user_id` or `email`. |
| `slack_replies` | Yes | `default_channel` | Reads replies from one Slack thread, optionally after a cursor timestamp. |
| `slack_channel_history` | Yes | `default_channel` | Reads channel timeline messages newer than a timestamp without expanding thread replies automatically. |
| `datadog_read` | Yes | none | Reads incidents, monitors, dashboards, metrics, log search results, log aggregates, and events through the server-owned Datadog client. |
| `github_read_repo` | Yes | none | Reads repository metadata, code search, tree or file contents, branches, and rulesets through the server-owned GitHub client. |
| `github_read_reviews` | Yes | none | Reads issues, pull requests, comments, reviews, commits, and comparisons through the server-owned GitHub client. |
| `github_read_ci` | Yes | none | Reads statuses, checks, workflows, runs, jobs, logs, and artifacts through the server-owned GitHub client. |

Tool notes:

- `slack_post` requires a `text` argument and accepts optional `channel` and `thread_ts`
- `slack_dm` requires a `text` argument plus exactly one of `user_id` or `email`
- `slack_replies` requires a `thread_ts` argument and accepts optional `channel` and `after_ts`
- `slack_channel_history` requires an `after_ts` argument and accepts optional `channel`, `before_ts`, and `max_messages`
- if `channel` is omitted at call time, `slack_post`, `slack_replies`, and `slack_channel_history` fall back to `tools[].config.default_channel`
- `slack_dm` does not accept tool config and does not use `default_channel`
- `slack_channel_history` returns chronological timeline entries from oldest to newest, preserves message `type` and `subtype`, and exposes `truncated` plus `next_cursor` when `max_messages` stops pagination early
- `slack_channel_history` is timeline-only; if a returned message has thread metadata and you need the replies, call `slack_replies` separately
- Slack read tools require a token with the relevant `*:history` scopes plus access to the target conversation; a user token can read public channels and any private conversation the user is a member of
- `slack_dm` needs a token that can open DMs and post messages, which typically means `im:write` plus `chat:write`; when using `email`, Slack also requires `users:read.email` and Slack recommends requesting it alongside `users:read`
- `slack_dm` caches successful normalized `email` -> `user_id` lookups in memory without eviction; cached mappings remain until the server process restarts
- `slack_post`, `slack_dm`, `slack_replies`, `slack_channel_history`, and `datadog_read` all require `network.reachable_hosts`
- `datadog_read` is a curated read-only tool, not a generic Datadog API proxy
- `query_metrics`, `search_logs`, and `aggregate_logs` require a `query` argument
- `search_logs` and `aggregate_logs` both accept optional `storage_tier: indexes | online-archives | flex` when you need to target a specific Datadog log tier
- `aggregate_logs` also accepts optional `indexes`, `compute`, `group_by`, and `page_cursor` inputs for Datadog log analytics queries
- `github_read_repo`, `github_read_reviews`, and `github_read_ci` all require `GITHUB_TOKEN` plus `network.reachable_hosts`
- every GitHub tool call requires explicit `action`, `owner`, and `repo` fields
- `github_read_repo` is repository-scoped only; it does not search across the whole of GitHub
- `github_read_repo.search_code` is discovery-only, remains scoped to the repository default branch, and should usually be followed by `get_file_contents` or `list_tree`
- `github_read_ci` log and artifact download actions write files under `github/actions/...` inside the agent root instead of inlining large payloads into the prompt
- `github_read_ci` download actions may follow GitHub-issued redirects, so agents that need logs or artifacts may need broader `network.reachable_hosts` than `api.github.com` alone
- `github_read_security`, `github_read_org`, and `github_write` are reserved follow-on tool IDs rather than stock tools in the current binary; security reads will need broader `security_events`-style access, org reads will need broader `read:org`-style access, and writes stay separate because they are mutating

Example:

```yaml
version: 1
model:
  name: gpt-5
prompt: |
  Watch the release channel for new deployment messages and summarize anything that looks risky.
network:
  reachable_hosts:
    - glob: slack.com
tools:
  - id: slack_channel_history
    config:
      default_channel: C12345678
```

Example tool call payload:

```json
{"after_ts":"1700.000001","max_messages":25}
```

```json
{"email":"ada@example.com","text":"Can you take a look at the deploy?"}
```

GitHub repo tool example:

```json
{"action":"search_code","owner":"acme","repo":"monorepo","query":"FlakyTest path:services/payments language:go","page":1,"per_page":25}
```

```json
{"tool":"github_read_repo","action":"search_code","ok":true,"data":{"query":"FlakyTest path:services/payments language:go repo:acme/monorepo","total_count":12,"incomplete_results":false,"items":[{"repository":"acme/monorepo","path":"services/payments/flaky_test.go","sha":"abc123","html_url":"https://github.com/acme/monorepo/blob/main/services/payments/flaky_test.go"}]},"warnings":["GitHub code search only indexes the default branch"],"page_info":{"page":1,"per_page":25,"has_next_page":false,"next_page":null},"files":[]}
```

GitHub review tool example:

```json
{"action":"get_pull_request","owner":"acme","repo":"monorepo","pull_number":9021}
```

```json
{"tool":"github_read_reviews","action":"get_pull_request","ok":true,"data":{"number":9021,"title":"Stabilize flaky payment retry test","state":"open","draft":false,"mergeable_state":"clean","head_sha":"abc123","head_ref":"fix/flaky-payment-retry","base_ref":"main","requested_reviewers":["alice"],"requested_teams":["payments"]},"warnings":[],"page_info":null,"files":[]}
```

GitHub CI tool example:

```json
{"action":"download_artifact","owner":"acme","repo":"monorepo","artifact_id":701,"extract":true}
```

```json
{"tool":"github_read_ci","action":"download_artifact","ok":true,"data":{"artifact_id":701,"name":"junit-results","redirected":true,"archive_format":"zip","extract":true},"warnings":[],"page_info":null,"files":[{"path":"github/actions/artifacts/701.zip","mime_type":"application/zip","description":"Raw artifact archive"},{"path":"github/actions/artifacts/701/junit.xml","mime_type":"application/xml","description":"Extracted JUnit report"}]}
```

Planned GitHub follow-on tools:

| Tool ID | Status | Extra scope or safety notes |
| --- | --- | --- |
| `github_read_security` | planned | Should stay separate from the baseline GitHub token because code scanning, Dependabot, and secret-scanning reads often need broader `security_events`-style access. |
| `github_read_org` | planned | Should stay separate from the baseline GitHub token because collaborator, team, and repository-permission reads often need broader `read:org`-style access. |
| `github_write` | planned | Should stay separate because it is mutating; keep it on a narrower first action set with stricter schemas and extra safety guidance around comments, issue updates, and review requests. |

Rules:

- custom tool ids are registry keys, not shell commands, file paths, URLs, or Go package paths
- built-in tools must not be listed in `tools`; they are already available
- disabled entries (`enabled: false`) are ignored during normalization
- unknown or duplicate custom tool ids fail validation
- if a custom tool requires network access, configure `network.reachable_hosts`

Managed HTTP headers are configured through `http.headers` and are injected into matching outbound interpreter-owned requests such as `curl`.

| Field | Type | Notes |
| --- | --- | --- |
| `http.headers[].name` | string | Header name to inject. |
| `http.headers[].value` | string | Literal header value. Exactly one of `value` or `env_var` must be set. |
| `http.headers[].env_var` | string | Reads the header value from an environment variable. Exactly one of `value` or `env_var` must be set. |
| `http.headers[].domains[].glob` | string | Host glob pattern such as `api.example.com`. |
| `http.headers[].domains[].regex` | string | Host regex pattern. |

If `domains` is omitted, the header applies to all outbound HTTP requests.
Each domain matcher must define exactly one of `glob` or `regex`.
Regex matchers may omit leading `^` and trailing `$`; the runtime anchors them internally.

### Network Sandboxing Examples

`swarmd` is deny-by-default. If you omit `network`, the agent gets no outbound access, and network-capable tools such as `http_request`, `read_web_page`, and `web_search` stay hidden.

Minimal agent with networking disabled:

```yaml
version: 1
model:
  name: gpt-5
prompt: |
  Read local files and write a short summary.
root_path: .
```

Allow exactly one API host:

```yaml
version: 1
model:
  name: gpt-5
prompt: |
  Fetch repository metadata from the GitHub API and write a report.
network:
  reachable_hosts:
    - glob: api.github.com
```

Allow a host and inject a server-owned header so the credential does not need to live in the prompt or workspace:

```yaml
version: 1
model:
  name: gpt-5
prompt: |
  Query the internal status API and summarize active incidents.
network:
  reachable_hosts:
    - glob: api.internal.example.com
http:
  headers:
    - name: X-API-Key
      env_var: INTERNAL_STATUS_API_KEY
      domains:
        - glob: api.internal.example.com
```

Allow a regex-defined family of hosts:

```yaml
network:
  reachable_hosts:
    - regex: ".*\\.datadoghq\\.(com|eu)"
```

If you intentionally want unrestricted outbound access, make that explicit:

```yaml
network:
  reachable_hosts:
    - glob: "*"
```

Rules of thumb:

- matchers apply to the host only, not full URLs, so use `api.github.com`, not `https://api.github.com`
- each `reachable_hosts` entry must define exactly one of `glob` or `regex`
- `http.headers` can be scoped per host and works well for server-owned credentials

## Runtime And Scheduling

Runtime fields tune how the agent loop behaves after it is loaded.
All duration fields use Go duration syntax such as `45s`, `1m`, or `2m30s`.

| Field | Type | Default | Notes |
| --- | --- | --- | --- |
| `runtime.desired_state` | string | `running` | Supported values are `running`, `paused`, and `stopped`. |
| `runtime.preserve_state` | bool | `false` | Keeps shell state warm between triggers when `true`. |
| `runtime.max_steps` | int | `32` | Maximum driver decisions per trigger. |
| `runtime.step_timeout` | duration string | `30s` | Timeout for a single shell step. |
| `runtime.max_output_bytes` | int | `65536` | Maximum captured bytes per stream per step. |
| `runtime.lease_duration` | duration string | `5m` | Mailbox lease length while an agent is working on a trigger. |
| `runtime.retry_delay` | duration string | `30s` | Delay before retrying after a failed run. |
| `runtime.max_attempts` | int | `5` | Maximum attempts before a message becomes dead-lettered. |

Schedules are defined under `schedules`:

| Field | Type | Default | Notes |
| --- | --- | --- | --- |
| `schedules[].id` | string | `<agent_id>-schedule-N` | Strongly recommended. Schedule ids must be unique within a namespace. |
| `schedules[].cron` | string | - | Parsed with the standard 5-field cron parser used by `robfig/cron`. |
| `schedules[].timezone` | string | `UTC` | Must be an IANA timezone name such as `UTC` or `America/Los_Angeles`. |
| `schedules[].enabled` | bool | `true` | Disabled schedules are stored but not fired. |
| `schedules[].payload` | any YAML value | auto-generated | Payload delivered to the agent when the schedule fires. |

If `payload` is omitted, the sync layer generates:

```yaml
kind: scheduled_run
agent_id: <agent_id>
schedule: <derived schedule id>
source: <yaml file name>
```

## Metadata And Free-Form Config

These fields are optional, but useful in most real agents:

| Field | Type | Notes |
| --- | --- | --- |
| `name` | string | Human-readable display name. |
| `description` | string | Stored as metadata with the agent record. |
| `config` | object | Free-form map stored with the agent record. |

`config` is persisted with the agent record, but custom tool configuration should live under `tools[].config` rather than top-level `config`.

## Top-Level Field Reference

| Field | Required | Type | Notes |
| --- | --- | --- | --- |
| `version` | Yes | int | Required agent YAML schema version. Currently only `1` is supported. |
| `agent_id` | No | string | Defaults to the YAML filename without the extension. Must be unique within a namespace. |
| `name` | No | string | Human-readable display name. |
| `description` | No | string | Stored as metadata with the agent record. |
| `model` | Yes | object | See the sections above for model fields. |
| `prompt` | Yes | string | Agent-specific instructions appended to the built-in runtime prompt. |
| `root_path` | No* | string | See Sandboxed Filesystem above. |
| `memory` | No | object | Optional persistent-memory guidance settings. |
| `mounts` | No | list | Optional files or directories materialized into the sandbox before each run. |
| `http` | No | object | Optional managed HTTP header rules. |
| `network` | No | object | Optional outbound host allowlist. Omit it entirely to disable networking. |
| `capabilities` | No | object | Free-form map. Today only `allow_message_send` changes runtime behavior. |
| `tools` | No | list | Explicit custom structured tools to add on top of the built-in tool surface. |
| `runtime` | No | object | Runtime tuning. |
| `schedules` | No | list | Optional cron schedules. |
| `config` | No | object | Free-form map stored with the agent record. |

\* `root_path` is optional when the server has a default `--data-dir` (defaults to `./data`) or an explicit `--root-base` / `SWARMD_SERVER_ROOT_BASE`. Otherwise sync fails unless `root_path` is set.

## Environment Variables And External Requirements

Some YAML features depend on environment variables provided to the server process:

| Feature | Variables |
| --- | --- |
| OpenAI-backed worker runtime | `OPENAI_API_KEY` |
| Anthropic-backed worker runtime | `ANTHROPIC_API_KEY` |
| Slack tools | `SLACK_USER_TOKEN` |
| Datadog tool | `DD_API_KEY`, `DD_APP_KEY`, optional `DD_SITE` |
| GitHub tools | `GITHUB_TOKEN`, optional `GITHUB_API_BASE_URL` |
| Managed HTTP headers from env | Whatever `http.headers[].env_var` references |
| Mount contents from env | Whatever `mounts[].source.env_var` references |

`swarmd config validate` and `swarmd server` both validate referenced tool and config environment variables when they can.

## Validation Rules

- agent specs must live exactly at `namespaces/<namespace>/agents/<file>.yaml`
- `version` must be present and currently must equal `1`
- `agent_id` must be safe to use as a directory name; only letters, numbers, `.`, `_`, and `-` are allowed
- `model.provider`, when set, must be `openai` or `anthropic`
- `model.name` must be present and non-empty
- `prompt` must be present and non-empty
- `tools` entries must be unique, must refer to registered custom tool ids, and must not list built-in tools
- every schedule must have a non-empty `cron`
- unknown fields are rejected because decoding uses strict field checking
- duration strings must parse with `time.ParseDuration`
- duplicate `agent_id` values within the same namespace are rejected
- during sync and plan, two agents cannot resolve to the same filesystem root

## Behavior Notes

- built-in structured tools are always available unless gated by capabilities such as networking; use `tools` only to add custom structured tools compiled into your fork
- when `network.reachable_hosts` is configured, the agent is told that `curl` and the HTTP tool surface can use the runtime-owned network dialer, limited to the configured hosts
- `server_log`, `slack_post`, `slack_replies`, and `slack_channel_history` are normal structured tools; they appear in the model-facing tool surface rather than as shell commands
- during sync, schedules for an agent are deleted and recreated from YAML, so set `schedules[].id` explicitly if you rely on stable schedule IDs
