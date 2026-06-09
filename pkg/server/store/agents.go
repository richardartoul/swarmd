package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// CreateAgent inserts an agent and its initial prompt version.
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
	stepTimeout := defaultDuration(params.StepTimeout, DefaultAgentStepTimeout)
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

// UpdateAgentPrompt appends a prompt version and makes it current.
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

// UpdateAgentDesiredState sets the operator-requested lifecycle state.
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

// GetAgent loads one agent with its current system prompt.
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

// ListRunnableAgents lists worker agents whose desired state is running.
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
