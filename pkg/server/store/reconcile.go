package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

func (s *Store) ListNamespaces(ctx context.Context) ([]Namespace, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT namespace_id, name, limits_json, created_at_ms, updated_at_ms FROM namespaces ORDER BY namespace_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("query namespaces: %w", err)
	}
	defer rows.Close()

	var namespaces []Namespace
	for rows.Next() {
		var namespace Namespace
		var createdAtMS int64
		var updatedAtMS int64
		if err := rows.Scan(&namespace.ID, &namespace.Name, &namespace.LimitsJSON, &createdAtMS, &updatedAtMS); err != nil {
			return nil, fmt.Errorf("scan namespace: %w", err)
		}
		namespace.CreatedAt = fromMillis(createdAtMS)
		namespace.UpdatedAt = fromMillis(updatedAtMS)
		namespaces = append(namespaces, namespace)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate namespaces: %w", err)
	}
	return namespaces, nil
}

func (s *Store) PutNamespace(ctx context.Context, params CreateNamespaceParams) (PutNamespaceResult, error) {
	namespaceID := defaultString(params.ID, NewID("namespace"))
	name := strings.TrimSpace(params.Name)
	if name == "" {
		name = namespaceID
	}
	limitsJSON, err := MarshalOptionalEnvelope("namespace_limits", params.Limits)
	if err != nil {
		return PutNamespaceResult{}, err
	}

	existing, err := s.GetNamespace(ctx, namespaceID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return PutNamespaceResult{}, err
	}
	if errors.Is(err, ErrNotFound) {
		namespace, err := s.CreateNamespace(ctx, CreateNamespaceParams{
			ID:     namespaceID,
			Name:   name,
			Limits: params.Limits,
		})
		if err != nil {
			return PutNamespaceResult{}, err
		}
		return PutNamespaceResult{Namespace: namespace, Created: true}, nil
	}

	updated := existing.Name != name || existing.LimitsJSON != limitsJSON
	if !updated {
		return PutNamespaceResult{Namespace: existing}, nil
	}
	now := s.now()
	if _, err := s.db.ExecContext(
		ctx,
		`UPDATE namespaces SET name = ?, limits_json = ?, updated_at_ms = ? WHERE namespace_id = ?`,
		name,
		limitsJSON,
		toMillis(now),
		namespaceID,
	); err != nil {
		return PutNamespaceResult{}, fmt.Errorf("update namespace %q: %w", namespaceID, err)
	}
	namespace, err := s.GetNamespace(ctx, namespaceID)
	if err != nil {
		return PutNamespaceResult{}, err
	}
	return PutNamespaceResult{Namespace: namespace, Updated: true}, nil
}

func (s *Store) PutAgent(ctx context.Context, params CreateAgentParams) (PutAgentResult, error) {
	normalized, configJSON, err := normalizeCreateAgentParams(params)
	if err != nil {
		return PutAgentResult{}, err
	}
	actionSchemaJSON, err := MarshalOptionalEnvelope("agent_action_schema", normalized.ActionSchema)
	if err != nil {
		return PutAgentResult{}, err
	}

	existing, err := s.GetAgent(ctx, normalized.NamespaceID, normalized.AgentID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return PutAgentResult{}, err
	}
	if errors.Is(err, ErrNotFound) {
		created, err := s.CreateAgent(ctx, normalized)
		if err != nil {
			return PutAgentResult{}, err
		}
		return PutAgentResult{Agent: created, Created: true}, nil
	}

	now := s.now()
	updated := existing.Name != defaultString(strings.TrimSpace(normalized.Name), normalized.AgentID) ||
		existing.Role != normalized.Role ||
		existing.DesiredState != normalized.DesiredState ||
		existing.RootPath != normalized.RootPath ||
		existing.ModelProvider != defaultString(strings.TrimSpace(normalized.ModelProvider), "openai") ||
		existing.ModelName != strings.TrimSpace(normalized.ModelName) ||
		existing.ModelBaseURL != strings.TrimSpace(normalized.ModelBaseURL) ||
		existing.PreserveState != normalized.PreserveState ||
		existing.MaxSteps != defaultInt(normalized.MaxSteps, 32) ||
		existing.StepTimeout != defaultDuration(normalized.StepTimeout, DefaultAgentStepTimeout) ||
		existing.MaxOutputBytes != defaultInt(normalized.MaxOutputBytes, 64<<10) ||
		existing.LeaseDuration != defaultDuration(normalized.LeaseDuration, 5*time.Minute) ||
		existing.RetryDelay != defaultDuration(normalized.RetryDelay, 30*time.Second) ||
		existing.MaxAttempts != defaultInt(normalized.MaxAttempts, 5) ||
		existing.ConfigJSON != configJSON

	if updated {
		if _, err := s.db.ExecContext(
			ctx,
			`UPDATE agents
			 SET name = ?, role = ?, desired_state = ?, root_path = ?, model_provider = ?, model_name = ?, model_base_url = ?,
			     sandbox_commands_json = ?, preserve_state = ?, max_steps = ?, step_timeout_millis = ?,
			     max_output_bytes = ?, lease_duration_millis = ?, retry_delay_millis = ?, max_attempts = ?, config_json = ?,
			     updated_at_ms = ?
			 WHERE namespace_id = ? AND agent_id = ?`,
			defaultString(strings.TrimSpace(normalized.Name), normalized.AgentID),
			string(normalized.Role),
			string(normalized.DesiredState),
			normalized.RootPath,
			defaultString(strings.TrimSpace(normalized.ModelProvider), "openai"),
			strings.TrimSpace(normalized.ModelName),
			strings.TrimSpace(normalized.ModelBaseURL),
			"",
			boolInt(normalized.PreserveState),
			defaultInt(normalized.MaxSteps, 32),
			toDurationMillis(defaultDuration(normalized.StepTimeout, DefaultAgentStepTimeout)),
			defaultInt(normalized.MaxOutputBytes, 64<<10),
			toDurationMillis(defaultDuration(normalized.LeaseDuration, 5*time.Minute)),
			toDurationMillis(defaultDuration(normalized.RetryDelay, 30*time.Second)),
			defaultInt(normalized.MaxAttempts, 5),
			configJSON,
			toMillis(now),
			normalized.NamespaceID,
			normalized.AgentID,
		); err != nil {
			return PutAgentResult{}, fmt.Errorf("update agent %q/%q: %w", normalized.NamespaceID, normalized.AgentID, err)
		}
	}

	prompt := strings.TrimSpace(normalized.SystemPrompt)
	if prompt == "" {
		prompt = "You are an agent managed by the SQLite server."
	}
	currentActionSchemaJSON, err := s.currentActionSchemaJSON(ctx, normalized.NamespaceID, existing.CurrentPromptVersionID)
	if err != nil {
		return PutAgentResult{}, err
	}
	if existing.SystemPrompt != prompt || currentActionSchemaJSON != actionSchemaJSON {
		if _, err := s.UpdateAgentPrompt(ctx, UpdateAgentPromptParams{
			NamespaceID:  normalized.NamespaceID,
			AgentID:      normalized.AgentID,
			Prompt:       prompt,
			ActionSchema: normalized.ActionSchema,
		}); err != nil {
			return PutAgentResult{}, err
		}
		updated = true
	}

	agentRecord, err := s.GetAgent(ctx, normalized.NamespaceID, normalized.AgentID)
	if err != nil {
		return PutAgentResult{}, err
	}
	return PutAgentResult{Agent: agentRecord, Updated: updated}, nil
}

func (s *Store) currentActionSchemaJSON(ctx context.Context, namespaceID, promptVersionID string) (string, error) {
	if strings.TrimSpace(promptVersionID) == "" {
		return "", nil
	}
	var actionSchemaJSON string
	err := s.db.QueryRowContext(
		ctx,
		`SELECT COALESCE(action_schema_json, '') FROM agent_prompt_versions WHERE namespace_id = ? AND prompt_version_id = ?`,
		namespaceID,
		promptVersionID,
	).Scan(&actionSchemaJSON)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("load action schema for prompt version %q/%q: %w", namespaceID, promptVersionID, err)
	default:
		return actionSchemaJSON, nil
	}
}

func (s *Store) DeleteAgent(ctx context.Context, namespaceID, agentID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM agents WHERE namespace_id = ? AND agent_id = ?`, namespaceID, agentID)
	if err != nil {
		return fmt.Errorf("delete agent %q/%q: %w", namespaceID, agentID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected deleting agent %q/%q: %w", namespaceID, agentID, err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteSchedulesByAgent(ctx context.Context, namespaceID, agentID string) (int, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM schedules WHERE namespace_id = ? AND agent_id = ?`, namespaceID, agentID)
	if err != nil {
		return 0, fmt.Errorf("delete schedules for agent %q/%q: %w", namespaceID, agentID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected deleting schedules for agent %q/%q: %w", namespaceID, agentID, err)
	}
	return int(affected), nil
}

func normalizeCreateAgentParams(params CreateAgentParams) (CreateAgentParams, string, error) {
	if strings.TrimSpace(params.NamespaceID) == "" {
		return CreateAgentParams{}, "", fmt.Errorf("create agent: namespace id must not be empty")
	}
	if strings.TrimSpace(params.RootPath) == "" {
		return CreateAgentParams{}, "", fmt.Errorf("create agent: root path must not be empty")
	}
	if strings.TrimSpace(params.AgentID) == "" {
		params.AgentID = NewID("agent")
	}
	if params.Role == "" {
		params.Role = AgentRoleWorker
	}
	if params.DesiredState == "" {
		params.DesiredState = AgentDesiredStateRunning
	}
	if strings.TrimSpace(params.ModelProvider) == "" {
		params.ModelProvider = "openai"
	}
	configJSON, err := MarshalOptionalEnvelope("agent_config", params.Config)
	if err != nil {
		return CreateAgentParams{}, "", err
	}
	filesystemKind, err := agentFilesystemKindFromConfig(configJSON)
	if err != nil {
		return CreateAgentParams{}, "", err
	}
	if filesystemKind != "memory" {
		if err := os.MkdirAll(params.RootPath, 0o755); err != nil {
			return CreateAgentParams{}, "", fmt.Errorf("create agent: ensure root path %q: %w", params.RootPath, err)
		}
	}
	return params, configJSON, nil
}

func agentFilesystemKindFromConfig(configJSON string) (string, error) {
	type filesystemConfig struct {
		Filesystem struct {
			Kind string `json:"kind,omitempty"`
		} `json:"filesystem,omitempty"`
	}
	var config filesystemConfig
	if err := DecodeEnvelopeInto(configJSON, &config); err != nil {
		return "", err
	}
	switch strings.TrimSpace(config.Filesystem.Kind) {
	case "", "disk":
		return "disk", nil
	case "memory":
		return "memory", nil
	default:
		return strings.TrimSpace(config.Filesystem.Kind), nil
	}
}
