package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

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
