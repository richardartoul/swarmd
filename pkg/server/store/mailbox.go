package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/richardartoul/swarmd/pkg/agent"
)

func (s *Store) EnqueueMessage(ctx context.Context, params CreateMailboxMessageParams) (MailboxMessageRecord, error) {
	if params.NamespaceID == "" {
		return MailboxMessageRecord{}, fmt.Errorf("enqueue message: namespace id must not be empty")
	}
	if params.RecipientAgentID == "" {
		return MailboxMessageRecord{}, fmt.Errorf("enqueue message: recipient agent id must not be empty")
	}
	now := s.now()
	messageID := defaultString(params.MessageID, NewID("msg"))
	threadID := defaultString(params.ThreadID, messageID)
	kind := defaultString(params.Kind, "mailbox.message")
	availableAt := params.AvailableAt
	if availableAt.IsZero() {
		availableAt = now
	}
	payloadJSON, err := MarshalEnvelope("mailbox_payload", params.Payload)
	if err != nil {
		return MailboxMessageRecord{}, err
	}
	metadataJSON, err := MarshalOptionalEnvelope("mailbox_metadata", params.Metadata)
	if err != nil {
		return MailboxMessageRecord{}, err
	}
	maxAttempts := defaultInt(params.MaxAttempts, 5)

	if _, err := s.db.ExecContext(
		ctx,
		`INSERT INTO mailbox_messages (
			namespace_id, message_id, thread_id, sender_agent_id, recipient_agent_id, kind, payload_json, metadata_json,
			status, available_at_ms, lease_owner, lease_expires_at_ms, attempt_count, max_attempts, run_id,
			dead_letter_reason, last_error, created_at_ms, updated_at_ms, claimed_at_ms, completed_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', NULL, 0, ?, '', '', '', ?, ?, NULL, NULL)`,
		params.NamespaceID,
		messageID,
		threadID,
		params.SenderAgentID,
		params.RecipientAgentID,
		kind,
		payloadJSON,
		metadataJSON,
		string(MailboxMessageStatusQueued),
		toMillis(availableAt),
		maxAttempts,
		toMillis(now),
		toMillis(now),
	); err != nil {
		return MailboxMessageRecord{}, fmt.Errorf("insert mailbox message %q: %w", messageID, err)
	}

	return MailboxMessageRecord{
		NamespaceID:      params.NamespaceID,
		ID:               messageID,
		ThreadID:         threadID,
		SenderAgentID:    params.SenderAgentID,
		RecipientAgentID: params.RecipientAgentID,
		Kind:             kind,
		PayloadJSON:      payloadJSON,
		MetadataJSON:     metadataJSON,
		Status:           MailboxMessageStatusQueued,
		AvailableAt:      availableAt.UTC(),
		MaxAttempts:      maxAttempts,
		CreatedAt:        now,
		UpdatedAt:        now,
	}, nil
}

func (s *Store) ClaimNextMessage(ctx context.Context, params ClaimMessageParams) (ClaimedMailboxMessage, error) {
	if params.NamespaceID == "" || params.AgentID == "" {
		return ClaimedMailboxMessage{}, fmt.Errorf("claim next message: namespace id and agent id must not be empty")
	}
	leaseOwner := defaultString(params.LeaseOwner, s.leaseOwner)
	leaseDuration := defaultDuration(params.LeaseDuration, 5*time.Minute)

	for range 8 {
		now := s.now()
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return ClaimedMailboxMessage{}, fmt.Errorf("begin claim tx: %w", err)
		}

		record, err := claimCandidate(ctx, tx, params.NamespaceID, params.AgentID, now)
		if err != nil {
			_ = tx.Rollback()
			if errors.Is(err, ErrNoAvailableMessage) {
				return ClaimedMailboxMessage{}, err
			}
			return ClaimedMailboxMessage{}, err
		}

		runID := NewID("run")
		leaseExpiresAt := now.Add(leaseDuration)
		res, err := tx.ExecContext(
			ctx,
			`UPDATE mailbox_messages
			 SET status = ?, lease_owner = ?, lease_expires_at_ms = ?, attempt_count = attempt_count + 1, run_id = ?, updated_at_ms = ?, claimed_at_ms = COALESCE(claimed_at_ms, ?)
			 WHERE namespace_id = ? AND message_id = ? AND status IN (?, ?) AND available_at_ms <= ? AND (status = ? OR lease_expires_at_ms IS NULL OR lease_expires_at_ms <= ?)`,
			string(MailboxMessageStatusLeased),
			leaseOwner,
			toMillis(leaseExpiresAt),
			runID,
			toMillis(now),
			toMillis(now),
			params.NamespaceID,
			record.ID,
			string(MailboxMessageStatusQueued),
			string(MailboxMessageStatusLeased),
			toMillis(now),
			string(MailboxMessageStatusQueued),
			toMillis(now),
		)
		if err != nil {
			_ = tx.Rollback()
			return ClaimedMailboxMessage{}, fmt.Errorf("lease mailbox message %q: %w", record.ID, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			_ = tx.Rollback()
			return ClaimedMailboxMessage{}, fmt.Errorf("rows affected leasing mailbox message %q: %w", record.ID, err)
		}
		if affected == 0 {
			_ = tx.Rollback()
			continue
		}

		triggerPrompt, err := renderRunTriggerPrompt(record.PayloadJSON)
		if err != nil {
			_ = tx.Rollback()
			return ClaimedMailboxMessage{}, fmt.Errorf("render trigger prompt for mailbox message %q: %w", record.ID, err)
		}

		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO runs (
				namespace_id, run_id, message_id, agent_id, trigger_id, status, started_at_ms, finished_at_ms,
				duration_millis, cwd, usage_cached_tokens, value_json, error, trigger_prompt, system_prompt, created_at_ms, updated_at_ms
			) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, 0, '', 0, '', '', ?, ?, ?, ?)`,
			params.NamespaceID,
			runID,
			record.ID,
			params.AgentID,
			record.ID,
			string(RunStatusRunning),
			toMillis(now),
			triggerPrompt,
			params.SystemPrompt,
			toMillis(now),
			toMillis(now),
		); err != nil {
			_ = tx.Rollback()
			return ClaimedMailboxMessage{}, fmt.Errorf("insert run %q for mailbox message %q: %w", runID, record.ID, err)
		}

		if err := tx.Commit(); err != nil {
			return ClaimedMailboxMessage{}, fmt.Errorf("commit claim for mailbox message %q: %w", record.ID, err)
		}

		record.Status = MailboxMessageStatusLeased
		record.LeaseOwner = leaseOwner
		record.LeaseExpiresAt = &leaseExpiresAt
		record.AttemptCount++
		record.RunID = runID
		record.UpdatedAt = now
		if record.ClaimedAt == nil {
			record.ClaimedAt = &now
		}
		return ClaimedMailboxMessage{
			Message: record,
			Run: RunRecord{
				NamespaceID:   params.NamespaceID,
				ID:            runID,
				MessageID:     record.ID,
				AgentID:       params.AgentID,
				TriggerID:     record.ID,
				Status:        string(RunStatusRunning),
				StartedAt:     now,
				TriggerPrompt: triggerPrompt,
				SystemPrompt:  params.SystemPrompt,
				CreatedAt:     now,
				UpdatedAt:     now,
			},
		}, nil
	}
	return ClaimedMailboxMessage{}, ErrNoAvailableMessage
}

func (s *Store) RecordStep(ctx context.Context, step StepRecord) error {
	if step.NamespaceID == "" || step.RunID == "" {
		return fmt.Errorf("record step: namespace id and run id must not be empty")
	}
	if _, err := s.db.ExecContext(
		ctx,
		`INSERT INTO steps (
			namespace_id, run_id, step_index, step_type, message_id, agent_id, thought, shell, action_name, action_tool_kind, action_input,
			action_output, action_output_truncated, usage_cached_tokens, cwd_before, cwd_after, stdout, stderr, stdout_truncated,
			stderr_truncated, started_at_ms, finished_at_ms, duration_millis, status, exit_status, error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		step.NamespaceID,
		step.RunID,
		step.StepIndex,
		step.StepType,
		step.MessageID,
		step.AgentID,
		step.Thought,
		step.Shell,
		step.ActionName,
		step.ActionToolKind,
		step.ActionInput,
		step.ActionOutput,
		boolInt(step.ActionOutputTruncated),
		step.UsageCachedTokens,
		step.CWDBefore,
		step.CWDAfter,
		step.Stdout,
		step.Stderr,
		boolInt(step.StdoutTruncated),
		boolInt(step.StderrTruncated),
		toMillis(step.StartedAt),
		toMillis(step.FinishedAt),
		toDurationMillis(step.Duration),
		step.Status,
		step.ExitStatus,
		step.Error,
	); err != nil {
		return fmt.Errorf("insert step %d for run %q: %w", step.StepIndex, step.RunID, err)
	}
	return nil
}

func (s *Store) CompleteRun(ctx context.Context, params CompleteRunParams) error {
	if params.NamespaceID == "" || params.RunID == "" || params.MessageID == "" {
		return fmt.Errorf("complete run: namespace id, run id, and message id must not be empty")
	}
	now := s.now()
	valueJSON, err := MarshalOptionalEnvelope("run_value", params.Value)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin complete run tx: %w", err)
	}
	defer tx.Rollback()

	runRes, err := tx.ExecContext(
		ctx,
		`UPDATE runs
		 SET status = ?, finished_at_ms = ?, duration_millis = ?, cwd = ?, usage_cached_tokens = ?, value_json = ?, error = ?, updated_at_ms = ?
		 WHERE namespace_id = ? AND run_id = ?`,
		params.Status,
		toMillis(params.FinishedAt),
		toDurationMillis(params.Duration),
		params.CWD,
		params.UsageCachedTokens,
		valueJSON,
		params.Error,
		toMillis(now),
		params.NamespaceID,
		params.RunID,
	)
	if err != nil {
		return fmt.Errorf("update run %q: %w", params.RunID, err)
	}
	affected, err := runRes.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected updating run %q: %w", params.RunID, err)
	}
	if affected == 0 {
		return ErrNotFound
	}

	messageStatus := MailboxMessageStatusCompleted
	availableAt := params.FinishedAt
	var leaseExpires any
	var completedAt any = toMillis(params.FinishedAt)
	deadLetterReason := ""
	if params.DeadLetterReason != "" {
		messageStatus = MailboxMessageStatusDeadLetter
		deadLetterReason = params.DeadLetterReason
	} else if params.RetryAt != nil {
		messageStatus = MailboxMessageStatusQueued
		availableAt = params.RetryAt.UTC()
		leaseExpires = nil
		completedAt = nil
	}
	messageRes, err := tx.ExecContext(
		ctx,
		`UPDATE mailbox_messages
		 SET status = ?, available_at_ms = ?, lease_owner = '', lease_expires_at_ms = ?, run_id = '', dead_letter_reason = ?, last_error = ?, updated_at_ms = ?, completed_at_ms = ?
		 WHERE namespace_id = ? AND message_id = ?`,
		string(messageStatus),
		toMillis(availableAt),
		leaseExpires,
		deadLetterReason,
		params.Error,
		toMillis(now),
		completedAt,
		params.NamespaceID,
		params.MessageID,
	)
	if err != nil {
		return fmt.Errorf("update mailbox message %q while completing run %q: %w", params.MessageID, params.RunID, err)
	}
	affected, err = messageRes.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected updating mailbox message %q: %w", params.MessageID, err)
	}
	if affected == 0 {
		return ErrNotFound
	}

	for _, message := range params.Outbox {
		record, err := enqueueMessageTx(ctx, tx, now, message)
		if err != nil {
			return fmt.Errorf("enqueue outbox message for run %q: %w", params.RunID, err)
		}
		_ = record
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit complete run %q: %w", params.RunID, err)
	}
	return nil
}

func (s *Store) GetRun(ctx context.Context, namespaceID, runID string) (RunRecord, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT namespace_id, run_id, message_id, agent_id, trigger_id, status, started_at_ms, finished_at_ms, duration_millis, cwd, usage_cached_tokens, value_json, error, trigger_prompt, system_prompt, created_at_ms, updated_at_ms
FROM runs
WHERE namespace_id = ? AND run_id = ?
`, namespaceID, runID)
	return scanRun(row)
}

func (s *Store) ListThreadMessages(ctx context.Context, namespaceID, threadID string, limit int) ([]MailboxThreadMessage, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT message_id, thread_id, sender_agent_id, recipient_agent_id, kind, payload_json, status, created_at_ms, completed_at_ms
FROM mailbox_messages
WHERE namespace_id = ? AND thread_id = ?
ORDER BY created_at_ms ASC
LIMIT ?
`, namespaceID, threadID, limit)
	if err != nil {
		return nil, fmt.Errorf("query thread messages for %q/%q: %w", namespaceID, threadID, err)
	}
	defer rows.Close()

	var messages []MailboxThreadMessage
	for rows.Next() {
		var record MailboxThreadMessage
		var status string
		var createdAtMS int64
		var completedAt sql.NullInt64
		if err := rows.Scan(
			&record.ID,
			&record.ThreadID,
			&record.SenderAgentID,
			&record.RecipientAgentID,
			&record.Kind,
			&record.PayloadJSON,
			&status,
			&createdAtMS,
			&completedAt,
		); err != nil {
			return nil, fmt.Errorf("scan thread message: %w", err)
		}
		record.Status = MailboxMessageStatus(status)
		record.CreatedAt = fromMillis(createdAtMS)
		record.CompletedAt = nullMillisToTime(completedAt)
		messages = append(messages, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate thread messages for %q/%q: %w", namespaceID, threadID, err)
	}
	return messages, nil
}

func claimCandidate(ctx context.Context, tx *sql.Tx, namespaceID, agentID string, now time.Time) (MailboxMessageRecord, error) {
	row := tx.QueryRowContext(ctx, `
SELECT namespace_id, message_id, thread_id, sender_agent_id, recipient_agent_id, kind, payload_json, metadata_json, status,
       available_at_ms, lease_owner, lease_expires_at_ms, attempt_count, max_attempts, run_id, dead_letter_reason,
       last_error, created_at_ms, updated_at_ms, claimed_at_ms, completed_at_ms
FROM mailbox_messages
WHERE namespace_id = ? AND recipient_agent_id = ? AND status IN (?, ?) AND available_at_ms <= ? AND (status = ? OR lease_expires_at_ms IS NULL OR lease_expires_at_ms <= ?)
ORDER BY available_at_ms ASC, created_at_ms ASC
LIMIT 1
`, namespaceID, agentID, string(MailboxMessageStatusQueued), string(MailboxMessageStatusLeased), toMillis(now), string(MailboxMessageStatusQueued), toMillis(now))
	record, err := scanMailboxMessage(row)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return MailboxMessageRecord{}, ErrNoAvailableMessage
		}
		return MailboxMessageRecord{}, err
	}
	return record, nil
}

func enqueueMessageTx(ctx context.Context, tx *sql.Tx, now time.Time, params CreateMailboxMessageParams) (MailboxMessageRecord, error) {
	messageID := defaultString(params.MessageID, NewID("msg"))
	threadID := defaultString(params.ThreadID, messageID)
	kind := defaultString(params.Kind, "mailbox.message")
	availableAt := params.AvailableAt
	if availableAt.IsZero() {
		availableAt = now
	}
	payloadJSON, err := MarshalEnvelope("mailbox_payload", params.Payload)
	if err != nil {
		return MailboxMessageRecord{}, err
	}
	metadataJSON, err := MarshalOptionalEnvelope("mailbox_metadata", params.Metadata)
	if err != nil {
		return MailboxMessageRecord{}, err
	}
	maxAttempts := defaultInt(params.MaxAttempts, 5)
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO mailbox_messages (
			namespace_id, message_id, thread_id, sender_agent_id, recipient_agent_id, kind, payload_json, metadata_json,
			status, available_at_ms, lease_owner, lease_expires_at_ms, attempt_count, max_attempts, run_id,
			dead_letter_reason, last_error, created_at_ms, updated_at_ms, claimed_at_ms, completed_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', NULL, 0, ?, '', '', '', ?, ?, NULL, NULL)`,
		params.NamespaceID,
		messageID,
		threadID,
		params.SenderAgentID,
		params.RecipientAgentID,
		kind,
		payloadJSON,
		metadataJSON,
		string(MailboxMessageStatusQueued),
		toMillis(availableAt),
		maxAttempts,
		toMillis(now),
		toMillis(now),
	); err != nil {
		return MailboxMessageRecord{}, err
	}
	return MailboxMessageRecord{
		NamespaceID:      params.NamespaceID,
		ID:               messageID,
		ThreadID:         threadID,
		SenderAgentID:    params.SenderAgentID,
		RecipientAgentID: params.RecipientAgentID,
		Kind:             kind,
		PayloadJSON:      payloadJSON,
		MetadataJSON:     metadataJSON,
		Status:           MailboxMessageStatusQueued,
		AvailableAt:      availableAt.UTC(),
		MaxAttempts:      maxAttempts,
		CreatedAt:        now,
		UpdatedAt:        now,
	}, nil
}

func scanMailboxMessage(scanner interface{ Scan(dest ...any) error }) (MailboxMessageRecord, error) {
	var record MailboxMessageRecord
	var status string
	var availableAtMS int64
	var leaseExpiresAt sql.NullInt64
	var createdAtMS int64
	var updatedAtMS int64
	var claimedAt sql.NullInt64
	var completedAt sql.NullInt64
	if err := scanner.Scan(
		&record.NamespaceID,
		&record.ID,
		&record.ThreadID,
		&record.SenderAgentID,
		&record.RecipientAgentID,
		&record.Kind,
		&record.PayloadJSON,
		&record.MetadataJSON,
		&status,
		&availableAtMS,
		&record.LeaseOwner,
		&leaseExpiresAt,
		&record.AttemptCount,
		&record.MaxAttempts,
		&record.RunID,
		&record.DeadLetterReason,
		&record.LastError,
		&createdAtMS,
		&updatedAtMS,
		&claimedAt,
		&completedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MailboxMessageRecord{}, ErrNotFound
		}
		return MailboxMessageRecord{}, fmt.Errorf("scan mailbox message: %w", err)
	}
	record.Status = MailboxMessageStatus(status)
	record.AvailableAt = fromMillis(availableAtMS)
	record.LeaseExpiresAt = nullMillisToTime(leaseExpiresAt)
	record.CreatedAt = fromMillis(createdAtMS)
	record.UpdatedAt = fromMillis(updatedAtMS)
	record.ClaimedAt = nullMillisToTime(claimedAt)
	record.CompletedAt = nullMillisToTime(completedAt)
	return record, nil
}

func renderRunTriggerPrompt(payloadJSON string) (string, error) {
	payload, err := DecodeEnvelopeAny(payloadJSON)
	if err != nil {
		return "", err
	}
	return agent.RenderTriggerPrompt(payload)
}

func scanRun(scanner interface{ Scan(dest ...any) error }) (RunRecord, error) {
	var record RunRecord
	var finishedAt sql.NullInt64
	var startedAtMS int64
	var durationMS int64
	var createdAtMS int64
	var updatedAtMS int64
	if err := scanner.Scan(
		&record.NamespaceID,
		&record.ID,
		&record.MessageID,
		&record.AgentID,
		&record.TriggerID,
		&record.Status,
		&startedAtMS,
		&finishedAt,
		&durationMS,
		&record.CWD,
		&record.UsageCachedTokens,
		&record.ValueJSON,
		&record.Error,
		&record.TriggerPrompt,
		&record.SystemPrompt,
		&createdAtMS,
		&updatedAtMS,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RunRecord{}, ErrNotFound
		}
		return RunRecord{}, fmt.Errorf("scan run: %w", err)
	}
	record.StartedAt = fromMillis(startedAtMS)
	record.FinishedAt = nullMillisToTime(finishedAt)
	record.Duration = fromDurationMillis(durationMS)
	record.CreatedAt = fromMillis(createdAtMS)
	record.UpdatedAt = fromMillis(updatedAtMS)
	return record, nil
}
