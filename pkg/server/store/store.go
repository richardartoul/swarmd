package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
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

func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY)`); err != nil {
		return fmt.Errorf("ensure schema_migrations table: %w", err)
	}
	currentVersion, err := s.currentSchemaVersion(ctx)
	if err != nil {
		return err
	}
	for _, migration := range migrations {
		if migration.version <= currentVersion {
			continue
		}
		if migration.foreignKeysOff {
			if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
				return fmt.Errorf("disable foreign keys for migration %d: %w", migration.version, err)
			}
		}
		tx, err := s.db.BeginTx(ctx, nil)
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
		if migration.foreignKeysOff {
			if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
				return fmt.Errorf("re-enable foreign keys after migration %d: %w", migration.version, err)
			}
		}
	}
	return nil
}

func (s *Store) CreateNamespace(ctx context.Context, params CreateNamespaceParams) (Namespace, error) {
	now := s.now()
	namespaceID := defaultString(params.ID, NewID("namespace"))
	limitsJSON, err := MarshalOptionalEnvelope("namespace_limits", params.Limits)
	if err != nil {
		return Namespace{}, err
	}
	name := strings.TrimSpace(params.Name)
	if name == "" {
		name = namespaceID
	}
	if _, err := s.db.ExecContext(
		ctx,
		`INSERT INTO namespaces (namespace_id, name, limits_json, created_at_ms, updated_at_ms) VALUES (?, ?, ?, ?, ?)`,
		namespaceID,
		name,
		limitsJSON,
		toMillis(now),
		toMillis(now),
	); err != nil {
		return Namespace{}, fmt.Errorf("insert namespace %q: %w", namespaceID, err)
	}
	return Namespace{
		ID:         namespaceID,
		Name:       name,
		LimitsJSON: limitsJSON,
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}

func (s *Store) GetNamespace(ctx context.Context, namespaceID string) (Namespace, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT namespace_id, name, limits_json, created_at_ms, updated_at_ms FROM namespaces WHERE namespace_id = ?`,
		namespaceID,
	)
	var namespace Namespace
	var createdAtMS, updatedAtMS int64
	if err := row.Scan(&namespace.ID, &namespace.Name, &namespace.LimitsJSON, &createdAtMS, &updatedAtMS); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Namespace{}, ErrNotFound
		}
		return Namespace{}, fmt.Errorf("scan namespace %q: %w", namespaceID, err)
	}
	namespace.CreatedAt = fromMillis(createdAtMS)
	namespace.UpdatedAt = fromMillis(updatedAtMS)
	return namespace, nil
}

func (s *Store) CreateAgent(ctx context.Context, params CreateAgentParams) (RunnableAgent, error) {
	params, configJSON, err := normalizeCreateAgentParams(params)
	if err != nil {
		return RunnableAgent{}, err
	}
	now := s.now()
	agentID := defaultString(params.AgentID, NewID("agent"))
	role := params.Role
	if role == "" {
		role = AgentRoleWorker
	}
	desiredState := params.DesiredState
	if desiredState == "" {
		desiredState = AgentDesiredStateRunning
	}
	modelProvider := strings.TrimSpace(params.ModelProvider)
	if modelProvider == "" {
		modelProvider = "openai"
	}
	maxSteps := defaultInt(params.MaxSteps, 32)
	maxOutputBytes := defaultInt(params.MaxOutputBytes, 64<<10)
	maxAttempts := defaultInt(params.MaxAttempts, 5)
	stepTimeout := defaultDuration(params.StepTimeout, 30*time.Second)
	leaseDuration := defaultDuration(params.LeaseDuration, 5*time.Minute)
	retryDelay := defaultDuration(params.RetryDelay, 30*time.Second)
	actionSchemaJSON, err := MarshalOptionalEnvelope("agent_action_schema", params.ActionSchema)
	if err != nil {
		return RunnableAgent{}, err
	}
	promptVersionID := NewID("prompt")
	systemPrompt := strings.TrimSpace(params.SystemPrompt)
	if systemPrompt == "" {
		systemPrompt = "You are an agent managed by the SQLite server."
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunnableAgent{}, fmt.Errorf("begin create agent tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO agents (
			namespace_id, agent_id, name, role, desired_state, root_path, model_provider, model_name, model_base_url,
			sandbox_commands_json, preserve_state, max_steps, step_timeout_millis, max_output_bytes,
			lease_duration_millis, retry_delay_millis, max_attempts, config_json, current_prompt_version_id,
			created_at_ms, updated_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		params.NamespaceID,
		agentID,
		defaultString(strings.TrimSpace(params.Name), agentID),
		string(role),
		string(desiredState),
		params.RootPath,
		modelProvider,
		strings.TrimSpace(params.ModelName),
		strings.TrimSpace(params.ModelBaseURL),
		"",
		boolInt(params.PreserveState),
		maxSteps,
		toDurationMillis(stepTimeout),
		maxOutputBytes,
		toDurationMillis(leaseDuration),
		toDurationMillis(retryDelay),
		maxAttempts,
		configJSON,
		promptVersionID,
		toMillis(now),
		toMillis(now),
	); err != nil {
		return RunnableAgent{}, fmt.Errorf("insert agent %q: %w", agentID, err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO agent_prompt_versions (namespace_id, prompt_version_id, agent_id, version, prompt, action_schema_json, created_at_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		params.NamespaceID,
		promptVersionID,
		agentID,
		1,
		systemPrompt,
		actionSchemaJSON,
		toMillis(now),
	); err != nil {
		return RunnableAgent{}, fmt.Errorf("insert agent prompt version %q: %w", promptVersionID, err)
	}

	if err := tx.Commit(); err != nil {
		return RunnableAgent{}, fmt.Errorf("commit create agent %q: %w", agentID, err)
	}

	return RunnableAgent{
		AgentRecord: AgentRecord{
			NamespaceID:            params.NamespaceID,
			ID:                     agentID,
			Name:                   defaultString(strings.TrimSpace(params.Name), agentID),
			Role:                   role,
			DesiredState:           desiredState,
			RootPath:               params.RootPath,
			ModelProvider:          modelProvider,
			ModelName:              strings.TrimSpace(params.ModelName),
			ModelBaseURL:           strings.TrimSpace(params.ModelBaseURL),
			PreserveState:          params.PreserveState,
			MaxSteps:               maxSteps,
			StepTimeout:            stepTimeout,
			MaxOutputBytes:         maxOutputBytes,
			LeaseDuration:          leaseDuration,
			RetryDelay:             retryDelay,
			MaxAttempts:            maxAttempts,
			ConfigJSON:             configJSON,
			CurrentPromptVersionID: promptVersionID,
			CreatedAt:              now,
			UpdatedAt:              now,
		},
		SystemPrompt: systemPrompt,
	}, nil
}

func (s *Store) UpdateAgentPrompt(ctx context.Context, params UpdateAgentPromptParams) (AgentPromptVersion, error) {
	if strings.TrimSpace(params.NamespaceID) == "" || strings.TrimSpace(params.AgentID) == "" {
		return AgentPromptVersion{}, fmt.Errorf("update agent prompt: namespace id and agent id must not be empty")
	}
	prompt := strings.TrimSpace(params.Prompt)
	if prompt == "" {
		return AgentPromptVersion{}, fmt.Errorf("update agent prompt: prompt must not be empty")
	}
	actionSchemaJSON, err := MarshalOptionalEnvelope("agent_action_schema", params.ActionSchema)
	if err != nil {
		return AgentPromptVersion{}, err
	}
	now := s.now()
	promptVersionID := NewID("prompt")

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentPromptVersion{}, fmt.Errorf("begin update agent prompt tx: %w", err)
	}
	defer tx.Rollback()

	var nextVersion int
	if err := tx.QueryRowContext(
		ctx,
		`SELECT COALESCE(MAX(version), 0) + 1 FROM agent_prompt_versions WHERE namespace_id = ? AND agent_id = ?`,
		params.NamespaceID,
		params.AgentID,
	).Scan(&nextVersion); err != nil {
		return AgentPromptVersion{}, fmt.Errorf("load next prompt version for %q/%q: %w", params.NamespaceID, params.AgentID, err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO agent_prompt_versions (namespace_id, prompt_version_id, agent_id, version, prompt, action_schema_json, created_at_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		params.NamespaceID,
		promptVersionID,
		params.AgentID,
		nextVersion,
		prompt,
		actionSchemaJSON,
		toMillis(now),
	); err != nil {
		return AgentPromptVersion{}, fmt.Errorf("insert prompt version for %q/%q: %w", params.NamespaceID, params.AgentID, err)
	}
	res, err := tx.ExecContext(
		ctx,
		`UPDATE agents SET current_prompt_version_id = ?, updated_at_ms = ? WHERE namespace_id = ? AND agent_id = ?`,
		promptVersionID,
		toMillis(now),
		params.NamespaceID,
		params.AgentID,
	)
	if err != nil {
		return AgentPromptVersion{}, fmt.Errorf("update current prompt version for %q/%q: %w", params.NamespaceID, params.AgentID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return AgentPromptVersion{}, fmt.Errorf("rows affected updating current prompt version: %w", err)
	}
	if affected == 0 {
		return AgentPromptVersion{}, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return AgentPromptVersion{}, fmt.Errorf("commit update prompt %q/%q: %w", params.NamespaceID, params.AgentID, err)
	}
	return AgentPromptVersion{
		NamespaceID:      params.NamespaceID,
		ID:               promptVersionID,
		AgentID:          params.AgentID,
		Version:          nextVersion,
		Prompt:           prompt,
		ActionSchemaJSON: actionSchemaJSON,
		CreatedAt:        now,
	}, nil
}

func (s *Store) UpdateAgentDesiredState(ctx context.Context, params UpdateAgentDesiredStateParams) error {
	now := s.now()
	res, err := s.db.ExecContext(
		ctx,
		`UPDATE agents SET desired_state = ?, updated_at_ms = ? WHERE namespace_id = ? AND agent_id = ?`,
		string(params.DesiredState),
		toMillis(now),
		params.NamespaceID,
		params.AgentID,
	)
	if err != nil {
		return fmt.Errorf("update desired state for %q/%q: %w", params.NamespaceID, params.AgentID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected updating desired state for %q/%q: %w", params.NamespaceID, params.AgentID, err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetAgent(ctx context.Context, namespaceID, agentID string) (RunnableAgent, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT
	a.namespace_id, a.agent_id, a.name, a.role, a.desired_state, a.root_path, a.model_provider, a.model_name, a.model_base_url,
	a.sandbox_commands_json, a.preserve_state, a.max_steps, a.step_timeout_millis, a.max_output_bytes,
	a.lease_duration_millis, a.retry_delay_millis, a.max_attempts, a.config_json, a.current_prompt_version_id,
	a.created_at_ms, a.updated_at_ms,
	COALESCE(p.prompt, '')
FROM agents a
LEFT JOIN agent_prompt_versions p
	ON p.namespace_id = a.namespace_id AND p.prompt_version_id = a.current_prompt_version_id
WHERE a.namespace_id = ? AND a.agent_id = ?
`, namespaceID, agentID)
	agentRecord, prompt, err := scanRunnableAgent(row)
	if err != nil {
		return RunnableAgent{}, err
	}
	return RunnableAgent{AgentRecord: agentRecord, SystemPrompt: prompt}, nil
}

func (s *Store) ListRunnableAgents(ctx context.Context) ([]RunnableAgent, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT
	a.namespace_id, a.agent_id, a.name, a.role, a.desired_state, a.root_path, a.model_provider, a.model_name, a.model_base_url,
	a.sandbox_commands_json, a.preserve_state, a.max_steps, a.step_timeout_millis, a.max_output_bytes,
	a.lease_duration_millis, a.retry_delay_millis, a.max_attempts, a.config_json, a.current_prompt_version_id,
	a.created_at_ms, a.updated_at_ms,
	COALESCE(p.prompt, '')
FROM agents a
LEFT JOIN agent_prompt_versions p
	ON p.namespace_id = a.namespace_id AND p.prompt_version_id = a.current_prompt_version_id
WHERE a.role = ? AND a.desired_state = ?
ORDER BY a.namespace_id, a.agent_id
`, string(AgentRoleWorker), string(AgentDesiredStateRunning))
	if err != nil {
		return nil, fmt.Errorf("query runnable agents: %w", err)
	}
	defer rows.Close()

	var agents []RunnableAgent
	for rows.Next() {
		agentRecord, prompt, err := scanRunnableAgent(rows)
		if err != nil {
			return nil, err
		}
		agents = append(agents, RunnableAgent{AgentRecord: agentRecord, SystemPrompt: prompt})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate runnable agents: %w", err)
	}
	return agents, nil
}

func (s *Store) ListSchedules(ctx context.Context, namespaceID string) ([]ScheduleRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT namespace_id, schedule_id, agent_id, cron_expr, timezone, payload_json, enabled, next_fire_at_ms, last_fire_at_ms, created_at_ms, updated_at_ms
FROM schedules
WHERE namespace_id = ?
ORDER BY schedule_id
`, namespaceID)
	if err != nil {
		return nil, fmt.Errorf("query schedules for namespace %q: %w", namespaceID, err)
	}
	defer rows.Close()

	var schedules []ScheduleRecord
	for rows.Next() {
		record, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		schedules = append(schedules, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate schedules for namespace %q: %w", namespaceID, err)
	}
	return schedules, nil
}

func (s *Store) SnapshotNamespace(ctx context.Context, namespaceID string) (NamespaceSnapshot, error) {
	namespace, err := s.GetNamespace(ctx, namespaceID)
	if err != nil {
		return NamespaceSnapshot{}, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT
	a.namespace_id, a.agent_id, a.name, a.role, a.desired_state, a.root_path, a.model_provider, a.model_name, a.model_base_url,
	a.sandbox_commands_json, a.preserve_state, a.max_steps, a.step_timeout_millis, a.max_output_bytes,
	a.lease_duration_millis, a.retry_delay_millis, a.max_attempts, a.config_json, a.current_prompt_version_id,
	a.created_at_ms, a.updated_at_ms,
	COALESCE(p.prompt, '')
FROM agents a
LEFT JOIN agent_prompt_versions p
	ON p.namespace_id = a.namespace_id AND p.prompt_version_id = a.current_prompt_version_id
WHERE a.namespace_id = ?
ORDER BY a.agent_id
`, namespaceID)
	if err != nil {
		return NamespaceSnapshot{}, fmt.Errorf("query namespace agents for %q: %w", namespaceID, err)
	}
	defer rows.Close()

	var snapshot NamespaceSnapshot
	snapshot.Namespace = namespace
	for rows.Next() {
		agentRecord, prompt, err := scanRunnableAgent(rows)
		if err != nil {
			return NamespaceSnapshot{}, err
		}
		snapshot.Agents = append(snapshot.Agents, RunnableAgent{AgentRecord: agentRecord, SystemPrompt: prompt})
	}
	if err := rows.Err(); err != nil {
		return NamespaceSnapshot{}, fmt.Errorf("iterate namespace agents for %q: %w", namespaceID, err)
	}
	schedules, err := s.ListSchedules(ctx, namespaceID)
	if err != nil {
		return NamespaceSnapshot{}, err
	}
	snapshot.Schedules = schedules
	if err := s.db.QueryRowContext(ctx, `
SELECT
	COALESCE(SUM(CASE WHEN status = 'queued' THEN 1 ELSE 0 END), 0),
	COALESCE(SUM(CASE WHEN status = 'leased' THEN 1 ELSE 0 END), 0),
	COALESCE(SUM(CASE WHEN status = 'dead_letter' THEN 1 ELSE 0 END), 0),
	COALESCE(SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0)
FROM mailbox_messages
WHERE namespace_id = ?
`, namespaceID).Scan(
		&snapshot.Mailbox.Queued,
		&snapshot.Mailbox.Leased,
		&snapshot.Mailbox.DeadLetter,
		&snapshot.Mailbox.Completed,
	); err != nil {
		return NamespaceSnapshot{}, fmt.Errorf("load mailbox summary for %q: %w", namespaceID, err)
	}
	return snapshot, nil
}

func (s *Store) ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping sqlite database: %w", err)
	}
	return nil
}

func (s *Store) currentSchemaVersion(ctx context.Context) (int, error) {
	var current int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return 0, fmt.Errorf("query schema version: %w", err)
	}
	return current, nil
}

func normalizeSQLiteDSN(dsn string) string {
	return normalizeSQLiteDSNWithMode(dsn, false)
}

func ensureReadOnlySQLiteExists(dsn string) error {
	path, ok, err := sqliteLocalPathAbs(dsn)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	info, err := os.Stat(path)
	if err == nil {
		if info.IsDir() {
			return fmt.Errorf("sqlite database %q is a directory", path)
		}
		return nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("sqlite database %q does not exist", path)
	}
	return fmt.Errorf("stat sqlite database %q: %w", path, err)
}

func sqliteLocalPath(dsn string) (string, bool) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" || dsn == ":memory:" {
		return "", false
	}
	if !strings.HasPrefix(dsn, "file:") {
		return dsn, true
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme != "file" {
		return "", false
	}
	if strings.EqualFold(parsed.Query().Get("mode"), "memory") {
		return "", false
	}
	path := strings.TrimSpace(parsed.Path)
	if path == "" {
		path = strings.TrimSpace(parsed.Opaque)
	}
	if path == "" || path == ":memory:" || strings.HasPrefix(path, ":memory:") {
		return "", false
	}
	return path, true
}

func sqliteLocalPathAbs(dsn string) (string, bool, error) {
	path, ok := sqliteLocalPath(dsn)
	if !ok {
		return "", false, nil
	}
	if filepath.IsAbs(path) {
		return path, true, nil
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", false, fmt.Errorf("resolve sqlite path %q: %w", path, err)
	}
	return absPath, true, nil
}

func normalizeSQLiteDSNWithMode(dsn string, readOnly bool) string {
	if dsn == ":memory:" {
		return dsn
	}
	if !strings.HasPrefix(dsn, "file:") {
		if abs, err := filepath.Abs(dsn); err == nil {
			dsn = abs
		}
		dsn = (&url.URL{Scheme: "file", Path: dsn}).String()
	}

	parsed, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	query := parsed.Query()
	if readOnly && query.Get("mode") == "" {
		query.Set("mode", "ro")
	}
	query.Add("_pragma", "foreign_keys(ON)")
	if !readOnly {
		query.Add("_pragma", "journal_mode(WAL)")
		query.Add("_pragma", "synchronous(NORMAL)")
	}
	query.Add("_pragma", "busy_timeout(5000)")
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func scanRunnableAgent(scanner interface{ Scan(dest ...any) error }) (AgentRecord, string, error) {
	var record AgentRecord
	var role string
	var desiredState string
	var preserveState int
	var stepTimeoutMS int64
	var leaseDurationMS int64
	var retryDelayMS int64
	var createdAtMS int64
	var updatedAtMS int64
	var prompt string
	if err := scanner.Scan(
		&record.NamespaceID,
		&record.ID,
		&record.Name,
		&role,
		&desiredState,
		&record.RootPath,
		&record.ModelProvider,
		&record.ModelName,
		&record.ModelBaseURL,
		new(string),
		&preserveState,
		&record.MaxSteps,
		&stepTimeoutMS,
		&record.MaxOutputBytes,
		&leaseDurationMS,
		&retryDelayMS,
		&record.MaxAttempts,
		&record.ConfigJSON,
		&record.CurrentPromptVersionID,
		&createdAtMS,
		&updatedAtMS,
		&prompt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AgentRecord{}, "", ErrNotFound
		}
		return AgentRecord{}, "", fmt.Errorf("scan runnable agent: %w", err)
	}
	record.Role = AgentRole(role)
	record.DesiredState = AgentDesiredState(desiredState)
	record.PreserveState = intBool(preserveState)
	record.StepTimeout = fromDurationMillis(stepTimeoutMS)
	record.LeaseDuration = fromDurationMillis(leaseDurationMS)
	record.RetryDelay = fromDurationMillis(retryDelayMS)
	record.CreatedAt = fromMillis(createdAtMS)
	record.UpdatedAt = fromMillis(updatedAtMS)
	return record, prompt, nil
}

func scanSchedule(scanner interface{ Scan(dest ...any) error }) (ScheduleRecord, error) {
	var record ScheduleRecord
	var enabled int
	var nextFireAt sql.NullInt64
	var lastFireAt sql.NullInt64
	var createdAtMS int64
	var updatedAtMS int64
	if err := scanner.Scan(
		&record.NamespaceID,
		&record.ID,
		&record.AgentID,
		&record.CronExpr,
		&record.TimeZone,
		&record.PayloadJSON,
		&enabled,
		&nextFireAt,
		&lastFireAt,
		&createdAtMS,
		&updatedAtMS,
	); err != nil {
		return ScheduleRecord{}, fmt.Errorf("scan schedule: %w", err)
	}
	record.Enabled = intBool(enabled)
	record.NextFireAt = nullMillisToTime(nextFireAt)
	record.LastFireAt = nullMillisToTime(lastFireAt)
	record.CreatedAt = fromMillis(createdAtMS)
	record.UpdatedAt = fromMillis(updatedAtMS)
	return record, nil
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func defaultInt(value, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}

func defaultDuration(value, fallback time.Duration) time.Duration {
	if value == 0 {
		return fallback
	}
	return value
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func intBool(v int) bool {
	return v != 0
}

func toMillis(t time.Time) int64 {
	return t.UTC().UnixMilli()
}

func fromMillis(ms int64) time.Time {
	return time.UnixMilli(ms).UTC()
}

func nullMillisToTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromMillis(v.Int64)
	return &t
}

func toDurationMillis(d time.Duration) int64 {
	return d.Milliseconds()
}

func fromDurationMillis(ms int64) time.Duration {
	return time.Duration(ms) * time.Millisecond
}
