package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound           = errors.New("server/store: record not found")
	ErrNoAvailableMessage = errors.New("server/store: no available mailbox message")
)

type Store struct {
	db         *sql.DB
	now        func() time.Time
	leaseOwner string
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	return openStore(ctx, dsn, false)
}

func OpenReadOnly(ctx context.Context, dsn string) (*Store, error) {
	return openStore(ctx, dsn, true)
}

func openStore(ctx context.Context, dsn string, readOnly bool) (*Store, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("server/store: sqlite dsn must not be empty")
	}
	if readOnly {
		if err := ensureReadOnlySQLiteExists(dsn); err != nil {
			return nil, err
		}
	}
	sqliteDSN := normalizeSQLiteDSNWithMode(dsn, readOnly)
	db, err := sql.Open("sqlite", sqliteDSN)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	db.SetConnMaxLifetime(0)

	s := &Store{
		db:         db,
		now:        func() time.Time { return time.Now().UTC() },
		leaseOwner: NewID("lease"),
	}
	if err := s.ping(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if !readOnly {
		if err := s.Migrate(ctx); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) DB() *sql.DB {
	return s.db
}

func (s *Store) LeaseOwner() string {
	return s.leaseOwner
}

// Migrate applies any outstanding schema migrations.
//
// All statements run on one dedicated connection: PRAGMA foreign_keys is
// connection-scoped in SQLite, so toggling it through the pool would disable
// enforcement on an arbitrary pooled connection while the migration
// transaction itself ran with foreign keys still enabled.
func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY)`); err != nil {
		return fmt.Errorf("ensure schema_migrations table: %w", err)
	}
	var currentVersion int
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&currentVersion); err != nil {
		return fmt.Errorf("query schema version: %w", err)
	}
	for _, migration := range migrations {
		if migration.version <= currentVersion {
			continue
		}
		if err := applyMigration(ctx, conn, migration); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(ctx context.Context, conn *sql.Conn, migration migrationStep) error {
	if migration.foreignKeysOff {
		if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
			return fmt.Errorf("disable foreign keys for migration %d: %w", migration.version, err)
		}
	}
	err := applyMigrationTx(ctx, conn, migration)
	if migration.foreignKeysOff {
		if _, onErr := conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`); onErr != nil && err == nil {
			err = fmt.Errorf("re-enable foreign keys after migration %d: %w", migration.version, onErr)
		}
		if err == nil {
			err = checkForeignKeys(ctx, conn, migration.version)
		}
	}
	return err
}

func applyMigrationTx(ctx context.Context, conn *sql.Conn, migration migrationStep) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", migration.version, err)
	}
	if migration.apply != nil {
		if err := migration.apply(ctx, tx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", migration.version, err)
		}
	} else if _, err := tx.ExecContext(ctx, migration.sql); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("apply migration %d: %w", migration.version, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES (?)`, migration.version); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("record migration %d: %w", migration.version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", migration.version, err)
	}
	return nil
}

// checkForeignKeys verifies referential integrity after a migration that ran
// with foreign-key enforcement disabled (typically a table rebuild).
func checkForeignKeys(ctx context.Context, conn *sql.Conn, version int) error {
	rows, err := conn.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("foreign key check after migration %d: %w", version, err)
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowID sql.NullInt64
		var parent string
		var fkID int
		if err := rows.Scan(&table, &rowID, &parent, &fkID); err != nil {
			return fmt.Errorf("scan foreign key violation after migration %d: %w", version, err)
		}
		return fmt.Errorf(
			"migration %d left a foreign key violation: table %q references missing row in %q",
			version,
			table,
			parent,
		)
	}
	return rows.Err()
}

func (s *Store) ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping sqlite database: %w", err)
	}
	return nil
}
