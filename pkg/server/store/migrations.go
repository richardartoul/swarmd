package store

import (
	"context"
	"database/sql"
	"fmt"
)

type migration struct {
	version int
	sql     string
	apply   func(context.Context, *sql.Tx) error
}

var migrations = []migration{
	{
		version: 1,
		sql: `
CREATE TABLE IF NOT EXISTS schema_migrations (
	version INTEGER PRIMARY KEY
);

CREATE TABLE IF NOT EXISTS namespaces (
	namespace_id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	limits_json TEXT NOT NULL DEFAULT '',
	created_at_ms INTEGER NOT NULL,
	updated_at_ms INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS agents (
	namespace_id TEXT NOT NULL,
	agent_id TEXT NOT NULL,
	name TEXT NOT NULL,
	role TEXT NOT NULL,
	desired_state TEXT NOT NULL,
	root_path TEXT NOT NULL,
	model_provider TEXT NOT NULL,
	model_name TEXT NOT NULL,
	model_base_url TEXT NOT NULL DEFAULT '',
	allow_network INTEGER NOT NULL,
	preserve_state INTEGER NOT NULL,
	max_steps INTEGER NOT NULL,
	step_timeout_millis INTEGER NOT NULL,
	max_output_bytes INTEGER NOT NULL,
	lease_duration_millis INTEGER NOT NULL,
	retry_delay_millis INTEGER NOT NULL,
	max_attempts INTEGER NOT NULL,
	config_json TEXT NOT NULL DEFAULT '',
	current_prompt_version_id TEXT NOT NULL DEFAULT '',
	created_at_ms INTEGER NOT NULL,
	updated_at_ms INTEGER NOT NULL,
	PRIMARY KEY (namespace_id, agent_id),
	FOREIGN KEY (namespace_id) REFERENCES namespaces(namespace_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS agent_prompt_versions (
	namespace_id TEXT NOT NULL,
	prompt_version_id TEXT NOT NULL,
	agent_id TEXT NOT NULL,
	version INTEGER NOT NULL,
	prompt TEXT NOT NULL,
	action_schema_json TEXT NOT NULL DEFAULT '',
	created_at_ms INTEGER NOT NULL,
	PRIMARY KEY (namespace_id, prompt_version_id),
	UNIQUE (namespace_id, agent_id, version),
	FOREIGN KEY (namespace_id, agent_id) REFERENCES agents(namespace_id, agent_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS mailbox_messages (
	namespace_id TEXT NOT NULL,
	message_id TEXT NOT NULL,
	thread_id TEXT NOT NULL,
	sender_agent_id TEXT NOT NULL DEFAULT '',
	recipient_agent_id TEXT NOT NULL,
	kind TEXT NOT NULL,
	payload_json TEXT NOT NULL,
	metadata_json TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	available_at_ms INTEGER NOT NULL,
	lease_owner TEXT NOT NULL DEFAULT '',
	lease_expires_at_ms INTEGER,
	attempt_count INTEGER NOT NULL,
	max_attempts INTEGER NOT NULL,
	run_id TEXT NOT NULL DEFAULT '',
	dead_letter_reason TEXT NOT NULL DEFAULT '',
	last_error TEXT NOT NULL DEFAULT '',
	created_at_ms INTEGER NOT NULL,
	updated_at_ms INTEGER NOT NULL,
	claimed_at_ms INTEGER,
	completed_at_ms INTEGER,
	PRIMARY KEY (namespace_id, message_id),
	FOREIGN KEY (namespace_id, recipient_agent_id) REFERENCES agents(namespace_id, agent_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS schedules (
	namespace_id TEXT NOT NULL,
	schedule_id TEXT NOT NULL,
	agent_id TEXT NOT NULL,
	cron_expr TEXT NOT NULL,
	timezone TEXT NOT NULL,
	payload_json TEXT NOT NULL,
	enabled INTEGER NOT NULL,
	next_fire_at_ms INTEGER,
	last_fire_at_ms INTEGER,
	created_at_ms INTEGER NOT NULL,
	updated_at_ms INTEGER NOT NULL,
	PRIMARY KEY (namespace_id, schedule_id),
	FOREIGN KEY (namespace_id, agent_id) REFERENCES agents(namespace_id, agent_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS runs (
	namespace_id TEXT NOT NULL,
	run_id TEXT NOT NULL,
	message_id TEXT NOT NULL,
	agent_id TEXT NOT NULL,
	trigger_id TEXT NOT NULL,
	status TEXT NOT NULL,
	started_at_ms INTEGER NOT NULL,
	finished_at_ms INTEGER,
	duration_millis INTEGER NOT NULL,
	cwd TEXT NOT NULL DEFAULT '',
	usage_cached_tokens INTEGER NOT NULL,
	value_json TEXT NOT NULL DEFAULT '',
	error TEXT NOT NULL DEFAULT '',
	created_at_ms INTEGER NOT NULL,
	updated_at_ms INTEGER NOT NULL,
	PRIMARY KEY (namespace_id, run_id),
	FOREIGN KEY (namespace_id, message_id) REFERENCES mailbox_messages(namespace_id, message_id) ON DELETE CASCADE,
	FOREIGN KEY (namespace_id, agent_id) REFERENCES agents(namespace_id, agent_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS steps (
	namespace_id TEXT NOT NULL,
	run_id TEXT NOT NULL,
	step_index INTEGER NOT NULL,
	message_id TEXT NOT NULL,
	agent_id TEXT NOT NULL,
	thought TEXT NOT NULL,
	shell TEXT NOT NULL,
	usage_cached_tokens INTEGER NOT NULL,
	cwd_before TEXT NOT NULL,
	cwd_after TEXT NOT NULL,
	stdout TEXT NOT NULL,
	stderr TEXT NOT NULL,
	stdout_truncated INTEGER NOT NULL,
	stderr_truncated INTEGER NOT NULL,
	started_at_ms INTEGER NOT NULL,
	finished_at_ms INTEGER NOT NULL,
	duration_millis INTEGER NOT NULL,
	status TEXT NOT NULL,
	exit_status INTEGER NOT NULL,
	error TEXT NOT NULL,
	PRIMARY KEY (namespace_id, run_id, step_index),
	FOREIGN KEY (namespace_id, run_id) REFERENCES runs(namespace_id, run_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_agents_desired_state ON agents(role, desired_state, updated_at_ms);
CREATE INDEX IF NOT EXISTS idx_mailbox_recipient_available ON mailbox_messages(namespace_id, recipient_agent_id, status, available_at_ms, created_at_ms);
CREATE INDEX IF NOT EXISTS idx_mailbox_lease ON mailbox_messages(namespace_id, recipient_agent_id, lease_expires_at_ms);
CREATE INDEX IF NOT EXISTS idx_mailbox_thread ON mailbox_messages(namespace_id, thread_id, created_at_ms);
CREATE INDEX IF NOT EXISTS idx_mailbox_status ON mailbox_messages(namespace_id, status, available_at_ms);
CREATE INDEX IF NOT EXISTS idx_schedules_due ON schedules(enabled, next_fire_at_ms, updated_at_ms);
CREATE INDEX IF NOT EXISTS idx_runs_message ON runs(namespace_id, message_id, created_at_ms);
`,
	},
	{
		version: 2,
		sql: `
DROP TABLE IF EXISTS primary_actions;
DROP TABLE IF EXISTS primary_messages;
DROP TABLE IF EXISTS primary_sessions;
`,
	},
	{
		version: 3,
		sql: `
ALTER TABLE agents ADD COLUMN sandbox_commands_json TEXT NOT NULL DEFAULT '';
`,
	},
	{
		version: 4,
		sql: `
ALTER TABLE runs ADD COLUMN trigger_prompt TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN system_prompt TEXT NOT NULL DEFAULT '';
`,
	},
	{
		version: 5,
		apply:   applyLegacyNamespaceMigration,
	},
	{
		version: 6,
		sql:     renameServerMetadataKeysSQL,
	},
	{
		version: 7,
		apply:   applyStepActionColumnsMigration,
	},
	{
		version: 8,
		apply:   applyStepTypeColumnMigration,
	},
}

const legacyNamespaceRenameSQL = `
ALTER TABLE tenants RENAME TO namespaces;
ALTER TABLE namespaces RENAME COLUMN tenant_id TO namespace_id;

CREATE TABLE namespaces_new (
	namespace_id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	limits_json TEXT NOT NULL DEFAULT '',
	created_at_ms INTEGER NOT NULL,
	updated_at_ms INTEGER NOT NULL
);
INSERT INTO namespaces_new
SELECT
	CASE
		WHEN namespace_id LIKE 'tenant_%' THEN 'namespace_' || substr(namespace_id, 8)
		ELSE namespace_id
	END,
	name,
	REPLACE(limits_json, 'tenant_limits', 'namespace_limits'),
	created_at_ms,
	updated_at_ms
FROM namespaces;
DROP TABLE namespaces;
ALTER TABLE namespaces_new RENAME TO namespaces;

CREATE TABLE agents_new (
	namespace_id TEXT NOT NULL,
	agent_id TEXT NOT NULL,
	name TEXT NOT NULL,
	role TEXT NOT NULL,
	desired_state TEXT NOT NULL,
	root_path TEXT NOT NULL,
	model_provider TEXT NOT NULL,
	model_name TEXT NOT NULL,
	model_base_url TEXT NOT NULL DEFAULT '',
	allow_network INTEGER NOT NULL,
	sandbox_commands_json TEXT NOT NULL DEFAULT '',
	preserve_state INTEGER NOT NULL,
	max_steps INTEGER NOT NULL,
	step_timeout_millis INTEGER NOT NULL,
	max_output_bytes INTEGER NOT NULL,
	lease_duration_millis INTEGER NOT NULL,
	retry_delay_millis INTEGER NOT NULL,
	max_attempts INTEGER NOT NULL,
	config_json TEXT NOT NULL DEFAULT '',
	current_prompt_version_id TEXT NOT NULL DEFAULT '',
	created_at_ms INTEGER NOT NULL,
	updated_at_ms INTEGER NOT NULL,
	PRIMARY KEY (namespace_id, agent_id),
	FOREIGN KEY (namespace_id) REFERENCES namespaces(namespace_id) ON DELETE CASCADE
);
INSERT INTO agents_new
SELECT
	CASE
		WHEN tenant_id LIKE 'tenant_%' THEN 'namespace_' || substr(tenant_id, 8)
		ELSE tenant_id
	END,
	agent_id, name, role, desired_state, root_path, model_provider, model_name, model_base_url, allow_network,
	sandbox_commands_json, preserve_state, max_steps, step_timeout_millis, max_output_bytes, lease_duration_millis,
	retry_delay_millis, max_attempts, config_json, current_prompt_version_id, created_at_ms, updated_at_ms
FROM agents;
DROP TABLE agents;
ALTER TABLE agents_new RENAME TO agents;

CREATE TABLE agent_prompt_versions_new (
	namespace_id TEXT NOT NULL,
	prompt_version_id TEXT NOT NULL,
	agent_id TEXT NOT NULL,
	version INTEGER NOT NULL,
	prompt TEXT NOT NULL,
	action_schema_json TEXT NOT NULL DEFAULT '',
	created_at_ms INTEGER NOT NULL,
	PRIMARY KEY (namespace_id, prompt_version_id),
	UNIQUE (namespace_id, agent_id, version),
	FOREIGN KEY (namespace_id, agent_id) REFERENCES agents(namespace_id, agent_id) ON DELETE CASCADE
);
INSERT INTO agent_prompt_versions_new
SELECT
	CASE
		WHEN tenant_id LIKE 'tenant_%' THEN 'namespace_' || substr(tenant_id, 8)
		ELSE tenant_id
	END,
	prompt_version_id, agent_id, version, prompt, action_schema_json, created_at_ms
FROM agent_prompt_versions;
DROP TABLE agent_prompt_versions;
ALTER TABLE agent_prompt_versions_new RENAME TO agent_prompt_versions;

CREATE TABLE mailbox_messages_new (
	namespace_id TEXT NOT NULL,
	message_id TEXT NOT NULL,
	thread_id TEXT NOT NULL,
	sender_agent_id TEXT NOT NULL DEFAULT '',
	recipient_agent_id TEXT NOT NULL,
	kind TEXT NOT NULL,
	payload_json TEXT NOT NULL,
	metadata_json TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	available_at_ms INTEGER NOT NULL,
	lease_owner TEXT NOT NULL DEFAULT '',
	lease_expires_at_ms INTEGER,
	attempt_count INTEGER NOT NULL,
	max_attempts INTEGER NOT NULL,
	run_id TEXT NOT NULL DEFAULT '',
	dead_letter_reason TEXT NOT NULL DEFAULT '',
	last_error TEXT NOT NULL DEFAULT '',
	created_at_ms INTEGER NOT NULL,
	updated_at_ms INTEGER NOT NULL,
	claimed_at_ms INTEGER,
	completed_at_ms INTEGER,
	PRIMARY KEY (namespace_id, message_id),
	FOREIGN KEY (namespace_id, recipient_agent_id) REFERENCES agents(namespace_id, agent_id) ON DELETE CASCADE
);
INSERT INTO mailbox_messages_new
SELECT
	CASE
		WHEN tenant_id LIKE 'tenant_%' THEN 'namespace_' || substr(tenant_id, 8)
		ELSE tenant_id
	END,
	message_id, thread_id, sender_agent_id, recipient_agent_id, kind, payload_json,
	REPLACE(metadata_json, 'controlplane_tenant_id', 'controlplane_namespace_id'),
	status, available_at_ms, lease_owner, lease_expires_at_ms, attempt_count, max_attempts, run_id,
	dead_letter_reason, last_error, created_at_ms, updated_at_ms, claimed_at_ms, completed_at_ms
FROM mailbox_messages;
DROP TABLE mailbox_messages;
ALTER TABLE mailbox_messages_new RENAME TO mailbox_messages;

CREATE TABLE schedules_new (
	namespace_id TEXT NOT NULL,
	schedule_id TEXT NOT NULL,
	agent_id TEXT NOT NULL,
	cron_expr TEXT NOT NULL,
	timezone TEXT NOT NULL,
	payload_json TEXT NOT NULL,
	enabled INTEGER NOT NULL,
	next_fire_at_ms INTEGER,
	last_fire_at_ms INTEGER,
	created_at_ms INTEGER NOT NULL,
	updated_at_ms INTEGER NOT NULL,
	PRIMARY KEY (namespace_id, schedule_id),
	FOREIGN KEY (namespace_id, agent_id) REFERENCES agents(namespace_id, agent_id) ON DELETE CASCADE
);
INSERT INTO schedules_new
SELECT
	CASE
		WHEN tenant_id LIKE 'tenant_%' THEN 'namespace_' || substr(tenant_id, 8)
		ELSE tenant_id
	END,
	schedule_id, agent_id, cron_expr, timezone, payload_json, enabled, next_fire_at_ms, last_fire_at_ms,
	created_at_ms, updated_at_ms
FROM schedules;
DROP TABLE schedules;
ALTER TABLE schedules_new RENAME TO schedules;

CREATE TABLE runs_new (
	namespace_id TEXT NOT NULL,
	run_id TEXT NOT NULL,
	message_id TEXT NOT NULL,
	agent_id TEXT NOT NULL,
	trigger_id TEXT NOT NULL,
	status TEXT NOT NULL,
	started_at_ms INTEGER NOT NULL,
	finished_at_ms INTEGER,
	duration_millis INTEGER NOT NULL,
	cwd TEXT NOT NULL DEFAULT '',
	usage_cached_tokens INTEGER NOT NULL,
	value_json TEXT NOT NULL DEFAULT '',
	error TEXT NOT NULL DEFAULT '',
	trigger_prompt TEXT NOT NULL DEFAULT '',
	system_prompt TEXT NOT NULL DEFAULT '',
	created_at_ms INTEGER NOT NULL,
	updated_at_ms INTEGER NOT NULL,
	PRIMARY KEY (namespace_id, run_id),
	FOREIGN KEY (namespace_id, message_id) REFERENCES mailbox_messages(namespace_id, message_id) ON DELETE CASCADE,
	FOREIGN KEY (namespace_id, agent_id) REFERENCES agents(namespace_id, agent_id) ON DELETE CASCADE
);
INSERT INTO runs_new
SELECT
	CASE
		WHEN tenant_id LIKE 'tenant_%' THEN 'namespace_' || substr(tenant_id, 8)
		ELSE tenant_id
	END,
	run_id, message_id, agent_id, trigger_id, status, started_at_ms, finished_at_ms, duration_millis, cwd,
	usage_cached_tokens, value_json, error, trigger_prompt, system_prompt, created_at_ms, updated_at_ms
FROM runs;
DROP TABLE runs;
ALTER TABLE runs_new RENAME TO runs;

CREATE TABLE steps_new (
	namespace_id TEXT NOT NULL,
	run_id TEXT NOT NULL,
	step_index INTEGER NOT NULL,
	message_id TEXT NOT NULL,
	agent_id TEXT NOT NULL,
	thought TEXT NOT NULL,
	shell TEXT NOT NULL,
	usage_cached_tokens INTEGER NOT NULL,
	cwd_before TEXT NOT NULL,
	cwd_after TEXT NOT NULL,
	stdout TEXT NOT NULL,
	stderr TEXT NOT NULL,
	stdout_truncated INTEGER NOT NULL,
	stderr_truncated INTEGER NOT NULL,
	started_at_ms INTEGER NOT NULL,
	finished_at_ms INTEGER NOT NULL,
	duration_millis INTEGER NOT NULL,
	status TEXT NOT NULL,
	exit_status INTEGER NOT NULL,
	error TEXT NOT NULL,
	PRIMARY KEY (namespace_id, run_id, step_index),
	FOREIGN KEY (namespace_id, run_id) REFERENCES runs(namespace_id, run_id) ON DELETE CASCADE
);
INSERT INTO steps_new
SELECT
	CASE
		WHEN tenant_id LIKE 'tenant_%' THEN 'namespace_' || substr(tenant_id, 8)
		ELSE tenant_id
	END,
	run_id, step_index, message_id, agent_id, thought, shell, usage_cached_tokens, cwd_before, cwd_after,
	stdout, stderr, stdout_truncated, stderr_truncated, started_at_ms, finished_at_ms, duration_millis, status,
	exit_status, error
FROM steps;
DROP TABLE steps;
ALTER TABLE steps_new RENAME TO steps;

CREATE INDEX IF NOT EXISTS idx_agents_desired_state ON agents(role, desired_state, updated_at_ms);
CREATE INDEX IF NOT EXISTS idx_mailbox_recipient_available ON mailbox_messages(namespace_id, recipient_agent_id, status, available_at_ms, created_at_ms);
CREATE INDEX IF NOT EXISTS idx_mailbox_lease ON mailbox_messages(namespace_id, recipient_agent_id, lease_expires_at_ms);
CREATE INDEX IF NOT EXISTS idx_mailbox_thread ON mailbox_messages(namespace_id, thread_id, created_at_ms);
CREATE INDEX IF NOT EXISTS idx_mailbox_status ON mailbox_messages(namespace_id, status, available_at_ms);
CREATE INDEX IF NOT EXISTS idx_schedules_due ON schedules(enabled, next_fire_at_ms, updated_at_ms);
CREATE INDEX IF NOT EXISTS idx_runs_message ON runs(namespace_id, message_id, created_at_ms);
`

const renameServerMetadataKeysSQL = `
UPDATE mailbox_messages
SET metadata_json = REPLACE(
	REPLACE(
		REPLACE(
			REPLACE(
				REPLACE(
					REPLACE(
						REPLACE(
							REPLACE(
								REPLACE(metadata_json, 'controlplane_tenant_id', 'server_namespace_id'),
								'controlplane_namespace_id', 'server_namespace_id'
							),
							'controlplane_agent_id', 'server_agent_id'
						),
						'controlplane_message_id', 'server_message_id'
					),
					'controlplane_thread_id', 'server_thread_id'
				),
				'controlplane_run_id', 'server_run_id'
			),
			'controlplane_sender_agent_id', 'server_sender_agent_id'
		),
		'controlplane_attempt_count', 'server_attempt_count'
	),
	'controlplane_max_attempts', 'server_max_attempts'
)
WHERE instr(metadata_json, 'controlplane_') > 0;
`

func applyLegacyNamespaceMigration(ctx context.Context, tx *sql.Tx) error {
	legacySchemaExists, err := tableExists(ctx, tx, "tenants")
	if err != nil {
		return err
	}
	if !legacySchemaExists {
		return nil
	}
	if _, err := tx.ExecContext(ctx, legacyNamespaceRenameSQL); err != nil {
		return fmt.Errorf("rename legacy schema: %w", err)
	}
	return nil
}

func tableExists(ctx context.Context, tx *sql.Tx, tableName string) (bool, error) {
	var count int
	if err := tx.QueryRowContext(
		ctx,
		`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`,
		tableName,
	).Scan(&count); err != nil {
		return false, fmt.Errorf("check table %q: %w", tableName, err)
	}
	return count > 0, nil
}

func applyStepActionColumnsMigration(ctx context.Context, tx *sql.Tx) error {
	exists, err := tableExists(ctx, tx, "steps")
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	for _, column := range []struct {
		name       string
		definition string
	}{
		{name: "action_name", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "action_tool_kind", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "action_input", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "action_output", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "action_output_truncated", definition: "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := addColumnIfMissing(ctx, tx, "steps", column.name, column.definition); err != nil {
			return err
		}
	}
	return nil
}

func applyStepTypeColumnMigration(ctx context.Context, tx *sql.Tx) error {
	exists, err := tableExists(ctx, tx, "steps")
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	return addColumnIfMissing(ctx, tx, "steps", "step_type", "TEXT NOT NULL DEFAULT ''")
}

func addColumnIfMissing(ctx context.Context, tx *sql.Tx, tableName, columnName, definition string) error {
	exists, err := tableColumnExists(ctx, tx, tableName, columnName)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, tableName, columnName, definition)); err != nil {
		return fmt.Errorf("add column %q to %q: %w", columnName, tableName, err)
	}
	return nil
}
