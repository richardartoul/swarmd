package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateRenamesServerMetadataKeys(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "migration.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create schema_migrations error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES (5)`); err != nil {
		t.Fatalf("insert schema version error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE mailbox_messages (metadata_json TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatalf("create mailbox_messages error = %v", err)
	}
	if _, err := db.ExecContext(
		ctx,
		`INSERT INTO mailbox_messages (metadata_json) VALUES (?)`,
		`{"controlplane_tenant_id":"namespace-demo","controlplane_agent_id":"agent-demo","controlplane_message_id":"message-demo","controlplane_thread_id":"thread-demo","controlplane_run_id":"run-demo","controlplane_sender_agent_id":"sender-demo","controlplane_attempt_count":1,"controlplane_max_attempts":3}`,
	); err != nil {
		t.Fatalf("insert mailbox message error = %v", err)
	}

	store, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	var metadataJSON string
	if err := store.DB().QueryRowContext(ctx, `SELECT metadata_json FROM mailbox_messages`).Scan(&metadataJSON); err != nil {
		t.Fatalf("query migrated metadata_json error = %v", err)
	}
	if strings.Contains(metadataJSON, "controlplane_") {
		t.Fatalf("metadata_json still contains legacy keys: %s", metadataJSON)
	}
	for _, want := range []string{
		`"server_namespace_id":"namespace-demo"`,
		`"server_agent_id":"agent-demo"`,
		`"server_message_id":"message-demo"`,
		`"server_thread_id":"thread-demo"`,
		`"server_run_id":"run-demo"`,
		`"server_sender_agent_id":"sender-demo"`,
		`"server_attempt_count":1`,
		`"server_max_attempts":3`,
	} {
		if !strings.Contains(metadataJSON, want) {
			t.Fatalf("metadata_json = %s, want substring %s", metadataJSON, want)
		}
	}
}

func TestMigrateAddsStepActionColumns(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "migration-steps.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create schema_migrations error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES (6)`); err != nil {
		t.Fatalf("insert schema version error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `
CREATE TABLE steps (
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
	error TEXT NOT NULL
)`); err != nil {
		t.Fatalf("create steps error = %v", err)
	}

	store, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	for _, column := range []string{
		"step_type",
		"action_name",
		"action_tool_kind",
		"action_input",
		"action_output",
		"action_output_truncated",
	} {
		exists, err := tableColumnExists(ctx, store.DB(), "steps", column)
		if err != nil {
			t.Fatalf("tableColumnExists(%q) error = %v", column, err)
		}
		if !exists {
			t.Fatalf("steps table missing migrated column %q", column)
		}
	}
}

func TestMigrateAddsRunFinishThoughtColumn(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "migration-runs.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create schema_migrations error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES (8)`); err != nil {
		t.Fatalf("insert schema version error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `
CREATE TABLE runs (
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
	updated_at_ms INTEGER NOT NULL
)`); err != nil {
		t.Fatalf("create runs error = %v", err)
	}

	store, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	exists, err := tableColumnExists(ctx, store.DB(), "runs", "finish_thought")
	if err != nil {
		t.Fatalf("tableColumnExists(%q) error = %v", "finish_thought", err)
	}
	if !exists {
		t.Fatal("runs table missing migrated column finish_thought")
	}
}

func TestMigrateDropsAgentAllowNetworkColumn(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "migration-agents.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create schema_migrations error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES (9)`); err != nil {
		t.Fatalf("insert schema version error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `
CREATE TABLE namespaces (
	namespace_id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	limits_json TEXT NOT NULL DEFAULT '',
	created_at_ms INTEGER NOT NULL,
	updated_at_ms INTEGER NOT NULL
)`); err != nil {
		t.Fatalf("create namespaces error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `
CREATE TABLE agents (
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
	updated_at_ms INTEGER NOT NULL
)`); err != nil {
		t.Fatalf("create agents error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO namespaces (namespace_id, name, limits_json, created_at_ms, updated_at_ms)
VALUES ('default', 'default', '', 1, 1)`); err != nil {
		t.Fatalf("insert namespace error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO agents (
	namespace_id, agent_id, name, role, desired_state, root_path, model_provider, model_name, model_base_url,
	allow_network, sandbox_commands_json, preserve_state, max_steps, step_timeout_millis, max_output_bytes,
	lease_duration_millis, retry_delay_millis, max_attempts, config_json, current_prompt_version_id, created_at_ms, updated_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"default", "worker", "worker", "worker", "running", "/tmp/worker", "openai", "gpt-5", "",
		1, "", 0, 8, 30000, 65536, 300000, 30000, 3, `{"version":1,"type":"agent_config","body":{}}`, "", 1, 1,
	); err != nil {
		t.Fatalf("insert agent error = %v", err)
	}

	store, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	exists, err := tableColumnExists(ctx, store.DB(), "agents", "allow_network")
	if err != nil {
		t.Fatalf("tableColumnExists(%q) error = %v", "allow_network", err)
	}
	if exists {
		t.Fatal("agents table still contains allow_network after migration")
	}
	var count int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(1) FROM agents WHERE namespace_id = 'default' AND agent_id = 'worker'`).Scan(&count); err != nil {
		t.Fatalf("query migrated agents error = %v", err)
	}
	if count != 1 {
		t.Fatalf("migrated agent count = %d, want 1", count)
	}
}
