package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

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
