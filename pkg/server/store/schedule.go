package store

import (
	"context"
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

func (s *Store) CreateSchedule(ctx context.Context, params CreateScheduleParams) (ScheduleRecord, error) {
	if params.NamespaceID == "" || params.AgentID == "" {
		return ScheduleRecord{}, fmt.Errorf("create schedule: namespace id and agent id must not be empty")
	}
	location, err := loadScheduleLocation(params.TimeZone)
	if err != nil {
		return ScheduleRecord{}, err
	}
	schedule, err := cron.ParseStandard(params.CronExpr)
	if err != nil {
		return ScheduleRecord{}, fmt.Errorf("create schedule: parse cron expression %q: %w", params.CronExpr, err)
	}
	now := s.now()
	enabled := params.Enabled
	if !params.Enabled {
		enabled = false
	} else {
		enabled = true
	}
	payloadJSON, err := MarshalEnvelope("schedule_payload", params.Payload)
	if err != nil {
		return ScheduleRecord{}, err
	}
	scheduleID := defaultString(params.ScheduleID, NewID("sched"))
	var nextFireAt *time.Time
	if enabled {
		next := schedule.Next(now.In(location)).UTC()
		nextFireAt = &next
	}
	if _, err := s.db.ExecContext(
		ctx,
		`INSERT INTO schedules (
			namespace_id, schedule_id, agent_id, cron_expr, timezone, payload_json, enabled, next_fire_at_ms, last_fire_at_ms, created_at_ms, updated_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)`,
		params.NamespaceID,
		scheduleID,
		params.AgentID,
		params.CronExpr,
		location.String(),
		payloadJSON,
		boolInt(enabled),
		timePtrToMillis(nextFireAt),
		toMillis(now),
		toMillis(now),
	); err != nil {
		return ScheduleRecord{}, fmt.Errorf("insert schedule %q: %w", scheduleID, err)
	}
	return ScheduleRecord{
		NamespaceID: params.NamespaceID,
		ID:          scheduleID,
		AgentID:     params.AgentID,
		CronExpr:    params.CronExpr,
		TimeZone:    location.String(),
		PayloadJSON: payloadJSON,
		Enabled:     enabled,
		NextFireAt:  nextFireAt,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

func (s *Store) DisableSchedule(ctx context.Context, namespaceID, scheduleID string) error {
	now := s.now()
	res, err := s.db.ExecContext(
		ctx,
		`UPDATE schedules SET enabled = 0, next_fire_at_ms = NULL, updated_at_ms = ? WHERE namespace_id = ? AND schedule_id = ?`,
		toMillis(now),
		namespaceID,
		scheduleID,
	)
	if err != nil {
		return fmt.Errorf("disable schedule %q/%q: %w", namespaceID, scheduleID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected disabling schedule %q/%q: %w", namespaceID, scheduleID, err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) FireDueSchedules(ctx context.Context, now time.Time, limit int) ([]MailboxMessageRecord, error) {
	if limit <= 0 {
		limit = 32
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT namespace_id, schedule_id, agent_id, cron_expr, timezone, payload_json, enabled, next_fire_at_ms, last_fire_at_ms, created_at_ms, updated_at_ms
FROM schedules
WHERE enabled = 1 AND next_fire_at_ms IS NOT NULL AND next_fire_at_ms <= ?
ORDER BY next_fire_at_ms ASC, updated_at_ms ASC
LIMIT ?
`, toMillis(now), limit)
	if err != nil {
		return nil, fmt.Errorf("query due schedules: %w", err)
	}
	defer rows.Close()

	var due []ScheduleRecord
	for rows.Next() {
		record, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		due = append(due, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate due schedules: %w", err)
	}

	var enqueued []MailboxMessageRecord
	for _, record := range due {
		message, fired, err := s.fireSchedule(ctx, now, record)
		if err != nil {
			return nil, err
		}
		if fired {
			enqueued = append(enqueued, message)
		}
	}
	return enqueued, nil
}

func (s *Store) fireSchedule(ctx context.Context, now time.Time, record ScheduleRecord) (MailboxMessageRecord, bool, error) {
	if record.NextFireAt == nil {
		return MailboxMessageRecord{}, false, nil
	}
	location, err := loadScheduleLocation(record.TimeZone)
	if err != nil {
		return MailboxMessageRecord{}, false, err
	}
	parsed, err := cron.ParseStandard(record.CronExpr)
	if err != nil {
		return MailboxMessageRecord{}, false, fmt.Errorf("fire schedule %q/%q: parse cron expression %q: %w", record.NamespaceID, record.ID, record.CronExpr, err)
	}
	dueAt := record.NextFireAt.UTC()
	nextFire := parsed.Next(dueAt.In(location))
	for !nextFire.After(now.In(location)) {
		nextFire = parsed.Next(nextFire)
	}
	nextFire = nextFire.UTC()

	payload, err := DecodeEnvelopeAny(record.PayloadJSON)
	if err != nil {
		return MailboxMessageRecord{}, false, fmt.Errorf("decode schedule payload for %q/%q: %w", record.NamespaceID, record.ID, err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MailboxMessageRecord{}, false, fmt.Errorf("begin fire schedule tx for %q/%q: %w", record.NamespaceID, record.ID, err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(
		ctx,
		`UPDATE schedules
		 SET last_fire_at_ms = ?, next_fire_at_ms = ?, updated_at_ms = ?
		 WHERE namespace_id = ? AND schedule_id = ? AND enabled = 1 AND next_fire_at_ms = ?`,
		toMillis(dueAt),
		toMillis(nextFire),
		toMillis(now),
		record.NamespaceID,
		record.ID,
		toMillis(dueAt),
	)
	if err != nil {
		return MailboxMessageRecord{}, false, fmt.Errorf("update fired schedule %q/%q: %w", record.NamespaceID, record.ID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return MailboxMessageRecord{}, false, fmt.Errorf("rows affected firing schedule %q/%q: %w", record.NamespaceID, record.ID, err)
	}
	if affected == 0 {
		return MailboxMessageRecord{}, false, nil
	}

	message, err := enqueueMessageTx(ctx, tx, now, CreateMailboxMessageParams{
		NamespaceID:      record.NamespaceID,
		ThreadID:         NewID("thread"),
		SenderAgentID:    "",
		RecipientAgentID: record.AgentID,
		Kind:             "schedule.fire",
		Payload:          payload,
		Metadata: map[string]any{
			"schedule_id": record.ID,
			"fired_at":    dueAt.Format(time.RFC3339Nano),
		},
		AvailableAt: now,
		MaxAttempts: 5,
	})
	if err != nil {
		return MailboxMessageRecord{}, false, fmt.Errorf("enqueue scheduled message for %q/%q: %w", record.NamespaceID, record.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return MailboxMessageRecord{}, false, fmt.Errorf("commit fired schedule %q/%q: %w", record.NamespaceID, record.ID, err)
	}
	return message, true, nil
}

func loadScheduleLocation(name string) (*time.Location, error) {
	if name == "" {
		return time.UTC, nil
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("load schedule timezone %q: %w", name, err)
	}
	return location, nil
}

func timePtrToMillis(t *time.Time) any {
	if t == nil {
		return nil
	}
	return toMillis(*t)
}
