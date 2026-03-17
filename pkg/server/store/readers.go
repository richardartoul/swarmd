package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

func (s *Store) ListAgents(ctx context.Context, params ListAgentsParams) ([]RunnableAgent, error) {
	query := `
SELECT
	a.namespace_id, a.agent_id, a.name, a.role, a.desired_state, a.root_path, a.model_provider, a.model_name, a.model_base_url,
	a.allow_network, a.sandbox_commands_json, a.preserve_state, a.max_steps, a.step_timeout_millis, a.max_output_bytes,
	a.lease_duration_millis, a.retry_delay_millis, a.max_attempts, a.config_json, a.current_prompt_version_id,
	a.created_at_ms, a.updated_at_ms,
	COALESCE(p.prompt, '')
FROM agents a
LEFT JOIN agent_prompt_versions p
	ON p.namespace_id = a.namespace_id AND p.prompt_version_id = a.current_prompt_version_id
`
	args := make([]any, 0, 1)
	if strings.TrimSpace(params.NamespaceID) != "" {
		query += "WHERE a.namespace_id = ?\n"
		args = append(args, params.NamespaceID)
	}
	query += "ORDER BY a.namespace_id, a.agent_id"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		if params.NamespaceID != "" {
			return nil, fmt.Errorf("query agents for namespace %q: %w", params.NamespaceID, err)
		}
		return nil, fmt.Errorf("query agents: %w", err)
	}
	defer rows.Close()

	var agents []RunnableAgent
	for rows.Next() {
		record, prompt, err := scanRunnableAgent(rows)
		if err != nil {
			return nil, err
		}
		agents = append(agents, RunnableAgent{AgentRecord: record, SystemPrompt: prompt})
	}
	if err := rows.Err(); err != nil {
		if params.NamespaceID != "" {
			return nil, fmt.Errorf("iterate agents for namespace %q: %w", params.NamespaceID, err)
		}
		return nil, fmt.Errorf("iterate agents: %w", err)
	}
	return agents, nil
}

func (s *Store) ListMailboxMessages(ctx context.Context, params ListMailboxMessagesParams) ([]MailboxMessageRecord, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = 50
	}

	query := `
SELECT namespace_id, message_id, thread_id, sender_agent_id, recipient_agent_id, kind, payload_json, metadata_json, status,
       available_at_ms, lease_owner, lease_expires_at_ms, attempt_count, max_attempts, run_id, dead_letter_reason,
       last_error, created_at_ms, updated_at_ms, claimed_at_ms, completed_at_ms
FROM mailbox_messages
`
	var conditions []string
	args := make([]any, 0, 5)
	if strings.TrimSpace(params.NamespaceID) != "" {
		conditions = append(conditions, "namespace_id = ?")
		args = append(args, params.NamespaceID)
	}
	if strings.TrimSpace(params.AgentID) != "" {
		conditions = append(conditions, "(recipient_agent_id = ? OR sender_agent_id = ?)")
		args = append(args, params.AgentID, params.AgentID)
	}
	if params.Status != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, string(params.Status))
	}
	if len(conditions) > 0 {
		query += "WHERE " + strings.Join(conditions, " AND ") + "\n"
	}
	query += "ORDER BY updated_at_ms DESC, created_at_ms DESC, message_id DESC\nLIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query mailbox messages: %w", err)
	}
	defer rows.Close()

	var messages []MailboxMessageRecord
	for rows.Next() {
		record, err := scanMailboxMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mailbox messages: %w", err)
	}
	return messages, nil
}

func (s *Store) GetMailboxMessage(ctx context.Context, namespaceID, messageID string) (MailboxMessageRecord, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT namespace_id, message_id, thread_id, sender_agent_id, recipient_agent_id, kind, payload_json, metadata_json, status,
       available_at_ms, lease_owner, lease_expires_at_ms, attempt_count, max_attempts, run_id, dead_letter_reason,
       last_error, created_at_ms, updated_at_ms, claimed_at_ms, completed_at_ms
FROM mailbox_messages
WHERE namespace_id = ? AND message_id = ?
`, namespaceID, messageID)
	return scanMailboxMessage(row)
}

func (s *Store) ListRuns(ctx context.Context, params ListRunsParams) ([]RunRecord, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = 50
	}

	query := `
SELECT namespace_id, run_id, message_id, agent_id, trigger_id, status, started_at_ms, finished_at_ms, duration_millis, cwd, usage_cached_tokens, value_json, error, trigger_prompt, system_prompt, created_at_ms, updated_at_ms
FROM runs
`
	var conditions []string
	args := make([]any, 0, 4)
	if strings.TrimSpace(params.NamespaceID) != "" {
		conditions = append(conditions, "namespace_id = ?")
		args = append(args, params.NamespaceID)
	}
	if strings.TrimSpace(params.AgentID) != "" {
		conditions = append(conditions, "agent_id = ?")
		args = append(args, params.AgentID)
	}
	if strings.TrimSpace(params.Status) != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, params.Status)
	}
	if len(conditions) > 0 {
		query += "WHERE " + strings.Join(conditions, " AND ") + "\n"
	}
	query += "ORDER BY started_at_ms DESC, created_at_ms DESC, run_id DESC\nLIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query runs: %w", err)
	}
	defer rows.Close()

	var runs []RunRecord
	for rows.Next() {
		record, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate runs: %w", err)
	}
	return runs, nil
}

func (s *Store) ListStepsByRun(ctx context.Context, namespaceID, runID string) ([]StepRecord, error) {
	hasActionColumns, err := stepActionColumnsAvailable(ctx, s.db)
	if err != nil {
		return nil, fmt.Errorf("inspect steps schema for run %q/%q: %w", namespaceID, runID, err)
	}
	hasStepTypeColumn, err := stepTypeColumnAvailable(ctx, s.db)
	if err != nil {
		return nil, fmt.Errorf("inspect step type schema for run %q/%q: %w", namespaceID, runID, err)
	}
	selectColumns := `namespace_id, run_id, message_id, agent_id, step_index, thought, shell, usage_cached_tokens, cwd_before, cwd_after,
       stdout, stderr, stdout_truncated, stderr_truncated, started_at_ms, finished_at_ms, duration_millis, status,
       exit_status, error`
	switch {
	case hasStepTypeColumn && hasActionColumns:
		selectColumns = `namespace_id, run_id, message_id, agent_id, step_index, step_type, thought, shell, action_name, action_tool_kind,
       action_input, action_output, action_output_truncated, usage_cached_tokens, cwd_before, cwd_after, stdout, stderr,
       stdout_truncated, stderr_truncated, started_at_ms, finished_at_ms, duration_millis, status, exit_status, error`
	case hasStepTypeColumn:
		selectColumns = `namespace_id, run_id, message_id, agent_id, step_index, step_type, thought, shell, usage_cached_tokens, cwd_before,
       cwd_after, stdout, stderr, stdout_truncated, stderr_truncated, started_at_ms, finished_at_ms, duration_millis, status,
       exit_status, error`
	case hasActionColumns:
		selectColumns = `namespace_id, run_id, message_id, agent_id, step_index, thought, shell, action_name, action_tool_kind, action_input,
       action_output, action_output_truncated, usage_cached_tokens, cwd_before, cwd_after, stdout, stderr, stdout_truncated,
       stderr_truncated, started_at_ms, finished_at_ms, duration_millis, status, exit_status, error`
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT `+selectColumns+`
FROM steps
WHERE namespace_id = ? AND run_id = ?
ORDER BY step_index ASC
`, namespaceID, runID)
	if err != nil {
		return nil, fmt.Errorf("query steps for run %q/%q: %w", namespaceID, runID, err)
	}
	defer rows.Close()

	var steps []StepRecord
	for rows.Next() {
		record, err := scanStep(rows, hasStepTypeColumn, hasActionColumns)
		if err != nil {
			return nil, err
		}
		steps = append(steps, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate steps for run %q/%q: %w", namespaceID, runID, err)
	}
	return steps, nil
}

func scanStep(scanner interface{ Scan(dest ...any) error }, hasStepTypeColumn, hasActionColumns bool) (StepRecord, error) {
	var record StepRecord
	var actionOutputTruncated int
	var stdoutTruncated int
	var stderrTruncated int
	var startedAtMS int64
	var finishedAtMS int64
	var durationMS int64
	var err error
	switch {
	case hasStepTypeColumn && hasActionColumns:
		err = scanner.Scan(
			&record.NamespaceID,
			&record.RunID,
			&record.MessageID,
			&record.AgentID,
			&record.StepIndex,
			&record.StepType,
			&record.Thought,
			&record.Shell,
			&record.ActionName,
			&record.ActionToolKind,
			&record.ActionInput,
			&record.ActionOutput,
			&actionOutputTruncated,
			&record.UsageCachedTokens,
			&record.CWDBefore,
			&record.CWDAfter,
			&record.Stdout,
			&record.Stderr,
			&stdoutTruncated,
			&stderrTruncated,
			&startedAtMS,
			&finishedAtMS,
			&durationMS,
			&record.Status,
			&record.ExitStatus,
			&record.Error,
		)
	case hasStepTypeColumn:
		err = scanner.Scan(
			&record.NamespaceID,
			&record.RunID,
			&record.MessageID,
			&record.AgentID,
			&record.StepIndex,
			&record.StepType,
			&record.Thought,
			&record.Shell,
			&record.UsageCachedTokens,
			&record.CWDBefore,
			&record.CWDAfter,
			&record.Stdout,
			&record.Stderr,
			&stdoutTruncated,
			&stderrTruncated,
			&startedAtMS,
			&finishedAtMS,
			&durationMS,
			&record.Status,
			&record.ExitStatus,
			&record.Error,
		)
	case hasActionColumns:
		err = scanner.Scan(
			&record.NamespaceID,
			&record.RunID,
			&record.MessageID,
			&record.AgentID,
			&record.StepIndex,
			&record.Thought,
			&record.Shell,
			&record.ActionName,
			&record.ActionToolKind,
			&record.ActionInput,
			&record.ActionOutput,
			&actionOutputTruncated,
			&record.UsageCachedTokens,
			&record.CWDBefore,
			&record.CWDAfter,
			&record.Stdout,
			&record.Stderr,
			&stdoutTruncated,
			&stderrTruncated,
			&startedAtMS,
			&finishedAtMS,
			&durationMS,
			&record.Status,
			&record.ExitStatus,
			&record.Error,
		)
	default:
		err = scanner.Scan(
			&record.NamespaceID,
			&record.RunID,
			&record.MessageID,
			&record.AgentID,
			&record.StepIndex,
			&record.Thought,
			&record.Shell,
			&record.UsageCachedTokens,
			&record.CWDBefore,
			&record.CWDAfter,
			&record.Stdout,
			&record.Stderr,
			&stdoutTruncated,
			&stderrTruncated,
			&startedAtMS,
			&finishedAtMS,
			&durationMS,
			&record.Status,
			&record.ExitStatus,
			&record.Error,
		)
	}
	if err != nil {
		if err == sql.ErrNoRows {
			return StepRecord{}, ErrNotFound
		}
		return StepRecord{}, fmt.Errorf("scan step: %w", err)
	}
	record.ActionOutputTruncated = intBool(actionOutputTruncated)
	record.StdoutTruncated = intBool(stdoutTruncated)
	record.StderrTruncated = intBool(stderrTruncated)
	record.StartedAt = fromMillis(startedAtMS)
	record.FinishedAt = fromMillis(finishedAtMS)
	record.Duration = fromDurationMillis(durationMS)
	return record, nil
}

func stepActionColumnsAvailable(ctx context.Context, query interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) (bool, error) {
	return tableColumnExists(ctx, query, "steps", "action_name")
}

func stepTypeColumnAvailable(ctx context.Context, query interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) (bool, error) {
	return tableColumnExists(ctx, query, "steps", "step_type")
}

func tableColumnExists(ctx context.Context, query interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, tableName, columnName string) (bool, error) {
	rows, err := query.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%s)`, tableName))
	if err != nil {
		return false, fmt.Errorf("query table_info for %q: %w", tableName, err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid        int
			name       string
			declType   string
			notNull    int
			defaultVal sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &declType, &notNull, &defaultVal, &primaryKey); err != nil {
			return false, fmt.Errorf("scan table_info for %q: %w", tableName, err)
		}
		if strings.EqualFold(name, columnName) {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate table_info for %q: %w", tableName, err)
	}
	return false, nil
}
