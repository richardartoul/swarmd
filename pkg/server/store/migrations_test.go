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
