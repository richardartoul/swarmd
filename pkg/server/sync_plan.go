package server

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/richardartoul/swarmd/pkg/agent"
	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
	"github.com/robfig/cron/v3"
)

type ChangeAction string

const (
	ChangeActionCreate ChangeAction = "create"
	ChangeActionUpdate ChangeAction = "update"
	ChangeActionDelete ChangeAction = "delete"
)

type FieldChange struct {
	Field  string
	Before string
	After  string
}

type AgentSpecSummary struct {
	Namespaces int
	Agents     int
	Schedules  int
}

type SyncPlanSummary struct {
	NamespacesCreated int
	NamespacesUpdated int
	AgentsCreated     int
	AgentsUpdated     int
	AgentsDeleted     int
	SchedulesCreated  int
	SchedulesUpdated  int
	SchedulesDeleted  int
}

type SyncPlan struct {
	Summary          SyncPlanSummary
	NamespaceChanges []NamespacePlanChange
	AgentChanges     []AgentPlanChange
	ScheduleChanges  []SchedulePlanChange
}

type NamespacePlanChange struct {
	Action      ChangeAction
	NamespaceID string
	Changes     []FieldChange
}

type AgentPlanChange struct {
	Action      ChangeAction
	NamespaceID string
	AgentID     string
	SourcePath  string
	RootPath    string
	ModelName   string
	Changes     []FieldChange
}

type SchedulePlanChange struct {
	Action      ChangeAction
	NamespaceID string
	AgentID     string
	ScheduleID  string
	SourcePath  string
	CronExpr    string
	TimeZone    string
	Enabled     bool
	Changes     []FieldChange
}

type desiredNamespaceState struct {
	NamespaceID string
	Name        string
	LimitsJSON  string
}

type desiredAgentState struct {
	NamespaceID    string
	AgentID        string
	Name           string
	Role           cpstore.AgentRole
	DesiredState   cpstore.AgentDesiredState
	RootPath       string
	ModelProvider  string
	ModelName      string
	ModelBaseURL   string
	PreserveState  bool
	MaxSteps       int
	StepTimeout    time.Duration
	MaxOutputBytes int
	LeaseDuration  time.Duration
	RetryDelay     time.Duration
	MaxAttempts    int
	ConfigJSON     string
	SystemPrompt   string
	SourcePath     string
}

type desiredScheduleState struct {
	NamespaceID string
	AgentID     string
	ScheduleID  string
	CronExpr    string
	TimeZone    string
	PayloadJSON string
	Enabled     bool
	SourcePath  string
}

func SummarizeAgentSpecs(specs []AgentSpec) AgentSpecSummary {
	namespaces := make(map[string]struct{}, len(specs))
	scheduleCount := 0
	for _, spec := range specs {
		namespaces[spec.NamespaceID] = struct{}{}
		scheduleCount += len(spec.Schedules)
	}
	return AgentSpecSummary{
		Namespaces: len(namespaces),
		Agents:     len(specs),
		Schedules:  scheduleCount,
	}
}

func (p SyncPlan) HasChanges() bool {
	return len(p.NamespaceChanges) > 0 || len(p.AgentChanges) > 0 || len(p.ScheduleChanges) > 0
}

func PlanSyncFromConfigRoot(ctx context.Context, store *cpstore.Store, configRoot, defaultRootBase string) (SyncPlan, error) {
	if store == nil {
		return SyncPlan{}, fmt.Errorf("sync plan requires a store")
	}
	specs, err := LoadAgentSpecs(configRoot)
	if err != nil {
		return SyncPlan{}, err
	}
	if err := validateUniqueAgentRoots(configRoot, defaultRootBase, specs); err != nil {
		return SyncPlan{}, err
	}

	desiredNamespaces := make(map[string]desiredNamespaceState)
	desiredAgents := make(map[string]desiredAgentState)
	desiredSchedules := make(map[string]desiredScheduleState)
	for _, spec := range specs {
		desiredNamespaces[spec.NamespaceID] = desiredNamespaceState{
			NamespaceID: spec.NamespaceID,
			Name:        spec.NamespaceID,
		}

		agentState, err := buildDesiredAgentState(configRoot, defaultRootBase, spec)
		if err != nil {
			return SyncPlan{}, err
		}
		agentKey := joinPlanKey(spec.NamespaceID, spec.AgentID)
		desiredAgents[agentKey] = agentState

		scheduleStates, err := buildDesiredScheduleStates(spec)
		if err != nil {
			return SyncPlan{}, err
		}
		for _, scheduleState := range scheduleStates {
			scheduleKey := joinPlanKey(scheduleState.NamespaceID, scheduleState.ScheduleID)
			if existing, ok := desiredSchedules[scheduleKey]; ok {
				return SyncPlan{}, fmt.Errorf(
					"duplicate schedule id %q for namespace %q in %q and %q",
					scheduleState.ScheduleID,
					scheduleState.NamespaceID,
					existing.SourcePath,
					scheduleState.SourcePath,
				)
			}
			desiredSchedules[scheduleKey] = scheduleState
		}
	}

	existingNamespaces, err := store.ListNamespaces(ctx)
	if err != nil {
		return SyncPlan{}, err
	}
	existingNamespacesByID := make(map[string]cpstore.Namespace, len(existingNamespaces))
	for _, namespace := range existingNamespaces {
		existingNamespacesByID[namespace.ID] = namespace
	}

	existingAgents, err := store.ListAgents(ctx, cpstore.ListAgentsParams{})
	if err != nil {
		return SyncPlan{}, err
	}
	existingAgentsByKey := make(map[string]cpstore.RunnableAgent, len(existingAgents))
	for _, agent := range existingAgents {
		existingAgentsByKey[joinPlanKey(agent.NamespaceID, agent.ID)] = agent
	}

	existingSchedulesByKey := make(map[string]cpstore.ScheduleRecord)
	for _, namespace := range existingNamespaces {
		schedules, err := store.ListSchedules(ctx, namespace.ID)
		if err != nil {
			return SyncPlan{}, err
		}
		for _, schedule := range schedules {
			existingSchedulesByKey[joinPlanKey(schedule.NamespaceID, schedule.ID)] = schedule
		}
	}

	plan := SyncPlan{}
	for namespaceID, desired := range desiredNamespaces {
		existing, ok := existingNamespacesByID[namespaceID]
		if !ok {
			plan.NamespaceChanges = append(plan.NamespaceChanges, NamespacePlanChange{
				Action:      ChangeActionCreate,
				NamespaceID: namespaceID,
			})
			plan.Summary.NamespacesCreated++
			continue
		}
		changes := diffNamespaceState(existing, desired)
		if len(changes) == 0 {
			continue
		}
		plan.NamespaceChanges = append(plan.NamespaceChanges, NamespacePlanChange{
			Action:      ChangeActionUpdate,
			NamespaceID: namespaceID,
			Changes:     changes,
		})
		plan.Summary.NamespacesUpdated++
	}

	for key, desired := range desiredAgents {
		existing, ok := existingAgentsByKey[key]
		if !ok {
			plan.AgentChanges = append(plan.AgentChanges, AgentPlanChange{
				Action:      ChangeActionCreate,
				NamespaceID: desired.NamespaceID,
				AgentID:     desired.AgentID,
				SourcePath:  desired.SourcePath,
				RootPath:    desired.RootPath,
				ModelName:   desired.ModelName,
			})
			plan.Summary.AgentsCreated++
			continue
		}
		changes := diffAgentState(existing, desired)
		if len(changes) == 0 {
			continue
		}
		plan.AgentChanges = append(plan.AgentChanges, AgentPlanChange{
			Action:      ChangeActionUpdate,
			NamespaceID: desired.NamespaceID,
			AgentID:     desired.AgentID,
			SourcePath:  desired.SourcePath,
			RootPath:    desired.RootPath,
			ModelName:   desired.ModelName,
			Changes:     changes,
		})
		plan.Summary.AgentsUpdated++
	}

	for key, existing := range existingAgentsByKey {
		if _, ok := desiredAgents[key]; ok {
			continue
		}
		plan.AgentChanges = append(plan.AgentChanges, AgentPlanChange{
			Action:      ChangeActionDelete,
			NamespaceID: existing.NamespaceID,
			AgentID:     existing.ID,
			RootPath:    existing.RootPath,
			ModelName:   existing.ModelName,
		})
		plan.Summary.AgentsDeleted++
	}

	for key, desired := range desiredSchedules {
		existing, ok := existingSchedulesByKey[key]
		if !ok {
			plan.ScheduleChanges = append(plan.ScheduleChanges, SchedulePlanChange{
				Action:      ChangeActionCreate,
				NamespaceID: desired.NamespaceID,
				AgentID:     desired.AgentID,
				ScheduleID:  desired.ScheduleID,
				SourcePath:  desired.SourcePath,
				CronExpr:    desired.CronExpr,
				TimeZone:    desired.TimeZone,
				Enabled:     desired.Enabled,
			})
			plan.Summary.SchedulesCreated++
			continue
		}
		changes := diffScheduleState(existing, desired)
		if len(changes) == 0 {
			continue
		}
		plan.ScheduleChanges = append(plan.ScheduleChanges, SchedulePlanChange{
			Action:      ChangeActionUpdate,
			NamespaceID: desired.NamespaceID,
			AgentID:     desired.AgentID,
			ScheduleID:  desired.ScheduleID,
			SourcePath:  desired.SourcePath,
			CronExpr:    desired.CronExpr,
			TimeZone:    desired.TimeZone,
			Enabled:     desired.Enabled,
			Changes:     changes,
		})
		plan.Summary.SchedulesUpdated++
	}

	for key, existing := range existingSchedulesByKey {
		if _, ok := desiredSchedules[key]; ok {
			continue
		}
		plan.ScheduleChanges = append(plan.ScheduleChanges, SchedulePlanChange{
			Action:      ChangeActionDelete,
			NamespaceID: existing.NamespaceID,
			AgentID:     existing.AgentID,
			ScheduleID:  existing.ID,
			CronExpr:    existing.CronExpr,
			TimeZone:    existing.TimeZone,
			Enabled:     existing.Enabled,
		})
		plan.Summary.SchedulesDeleted++
	}

	sort.Slice(plan.NamespaceChanges, func(i, j int) bool {
		return plan.NamespaceChanges[i].NamespaceID < plan.NamespaceChanges[j].NamespaceID
	})
	sort.Slice(plan.AgentChanges, func(i, j int) bool {
		if plan.AgentChanges[i].NamespaceID != plan.AgentChanges[j].NamespaceID {
			return plan.AgentChanges[i].NamespaceID < plan.AgentChanges[j].NamespaceID
		}
		if plan.AgentChanges[i].AgentID != plan.AgentChanges[j].AgentID {
			return plan.AgentChanges[i].AgentID < plan.AgentChanges[j].AgentID
		}
		return plan.AgentChanges[i].Action < plan.AgentChanges[j].Action
	})
	sort.Slice(plan.ScheduleChanges, func(i, j int) bool {
		if plan.ScheduleChanges[i].NamespaceID != plan.ScheduleChanges[j].NamespaceID {
			return plan.ScheduleChanges[i].NamespaceID < plan.ScheduleChanges[j].NamespaceID
		}
		if plan.ScheduleChanges[i].AgentID != plan.ScheduleChanges[j].AgentID {
			return plan.ScheduleChanges[i].AgentID < plan.ScheduleChanges[j].AgentID
		}
		if plan.ScheduleChanges[i].ScheduleID != plan.ScheduleChanges[j].ScheduleID {
			return plan.ScheduleChanges[i].ScheduleID < plan.ScheduleChanges[j].ScheduleID
		}
		return plan.ScheduleChanges[i].Action < plan.ScheduleChanges[j].Action
	})

	return plan, nil
}

func buildManagedAgentParams(configRoot, defaultRootBase string, spec AgentSpec) (cpstore.CreateAgentParams, error) {
	rootPath, err := resolveAgentRootPath(configRoot, defaultRootBase, spec)
	if err != nil {
		return cpstore.CreateAgentParams{}, fmt.Errorf("resolve root for %q/%q: %w", spec.NamespaceID, spec.AgentID, err)
	}
	normalizedTools, err := normalizeAgentTools(spec.Tools)
	if err != nil {
		return cpstore.CreateAgentParams{}, fmt.Errorf("normalize tools for %q/%q: %w", spec.NamespaceID, spec.AgentID, err)
	}
	config, err := managedAgentConfig(spec, normalizedTools)
	if err != nil {
		return cpstore.CreateAgentParams{}, fmt.Errorf("build config for %q/%q: %w", spec.NamespaceID, spec.AgentID, err)
	}
	stepTimeout, err := parseOptionalDuration(spec.Runtime.StepTimeout)
	if err != nil {
		return cpstore.CreateAgentParams{}, fmt.Errorf("parse step_timeout for %q/%q: %w", spec.NamespaceID, spec.AgentID, err)
	}
	leaseDuration, err := parseOptionalDuration(spec.Runtime.LeaseDuration)
	if err != nil {
		return cpstore.CreateAgentParams{}, fmt.Errorf("parse lease_duration for %q/%q: %w", spec.NamespaceID, spec.AgentID, err)
	}
	retryDelay, err := parseOptionalDuration(spec.Runtime.RetryDelay)
	if err != nil {
		return cpstore.CreateAgentParams{}, fmt.Errorf("parse retry_delay for %q/%q: %w", spec.NamespaceID, spec.AgentID, err)
	}
	actionSchema, err := agent.ResolveActionSchema(normalizedTools, agentNetworkHostMatchers(spec.Network))
	if err != nil {
		return cpstore.CreateAgentParams{}, fmt.Errorf("build action schema for %q/%q: %w", spec.NamespaceID, spec.AgentID, err)
	}
	return cpstore.CreateAgentParams{
		NamespaceID:    spec.NamespaceID,
		AgentID:        spec.AgentID,
		Name:           spec.Name,
		Role:           cpstore.AgentRoleWorker,
		DesiredState:   desiredStateFromSpec(spec.Runtime.DesiredState),
		RootPath:       rootPath,
		ModelProvider:  spec.Model.Provider,
		ModelName:      spec.Model.Name,
		ModelBaseURL:   spec.Model.BaseURL,
		PreserveState:  spec.Runtime.PreserveState,
		MaxSteps:       spec.Runtime.MaxSteps,
		StepTimeout:    stepTimeout,
		MaxOutputBytes: spec.Runtime.MaxOutputBytes,
		LeaseDuration:  leaseDuration,
		RetryDelay:     retryDelay,
		MaxAttempts:    spec.Runtime.MaxAttempts,
		Config:         config,
		SystemPrompt:   spec.Prompt,
		ActionSchema:   actionSchema,
	}, nil
}

func buildManagedScheduleParams(spec AgentSpec) ([]cpstore.CreateScheduleParams, error) {
	params := make([]cpstore.CreateScheduleParams, 0, len(spec.Schedules))
	seenIDs := make(map[string]int, len(spec.Schedules))
	for idx, schedule := range spec.Schedules {
		cronExpr := strings.TrimSpace(schedule.CronExpr)
		if _, err := cron.ParseStandard(cronExpr); err != nil {
			return nil, fmt.Errorf("parse cron for %q/%q schedule %d: %w", spec.NamespaceID, spec.AgentID, idx+1, err)
		}
		timeZone, err := normalizeScheduleTimeZone(schedule.TimeZone)
		if err != nil {
			return nil, fmt.Errorf("parse timezone for %q/%q schedule %d: %w", spec.NamespaceID, spec.AgentID, idx+1, err)
		}
		scheduleID := toolscommon.FirstNonEmptyString(schedule.ID, derivedScheduleID(spec.AgentID, idx))
		if previous, ok := seenIDs[scheduleID]; ok {
			return nil, fmt.Errorf(
				"duplicate schedule id %q for %q/%q at positions %d and %d",
				scheduleID,
				spec.NamespaceID,
				spec.AgentID,
				previous+1,
				idx+1,
			)
		}
		seenIDs[scheduleID] = idx
		enabled := true
		if schedule.Enabled != nil {
			enabled = *schedule.Enabled
		}
		payload := schedule.Payload
		if payload == nil {
			payload = defaultSchedulePayload(spec, idx)
		}
		params = append(params, cpstore.CreateScheduleParams{
			NamespaceID: spec.NamespaceID,
			ScheduleID:  scheduleID,
			AgentID:     spec.AgentID,
			CronExpr:    cronExpr,
			TimeZone:    timeZone,
			Payload:     payload,
			Enabled:     enabled,
		})
	}
	return params, nil
}

func buildDesiredAgentState(configRoot, defaultRootBase string, spec AgentSpec) (desiredAgentState, error) {
	params, err := buildManagedAgentParams(configRoot, defaultRootBase, spec)
	if err != nil {
		return desiredAgentState{}, err
	}
	configJSON, err := cpstore.MarshalOptionalEnvelope("agent_config", params.Config)
	if err != nil {
		return desiredAgentState{}, fmt.Errorf("encode config for %q/%q: %w", spec.NamespaceID, spec.AgentID, err)
	}
	return desiredAgentState{
		NamespaceID:    params.NamespaceID,
		AgentID:        params.AgentID,
		Name:           normalizedAgentName(params),
		Role:           normalizedAgentRole(params),
		DesiredState:   normalizedDesiredState(params),
		RootPath:       params.RootPath,
		ModelProvider:  normalizedModelProvider(params),
		ModelName:      strings.TrimSpace(params.ModelName),
		ModelBaseURL:   strings.TrimSpace(params.ModelBaseURL),
		PreserveState:  params.PreserveState,
		MaxSteps:       normalizedMaxSteps(params),
		StepTimeout:    normalizedStepTimeout(params),
		MaxOutputBytes: normalizedMaxOutputBytes(params),
		LeaseDuration:  normalizedLeaseDuration(params),
		RetryDelay:     normalizedRetryDelay(params),
		MaxAttempts:    normalizedMaxAttempts(params),
		ConfigJSON:     configJSON,
		SystemPrompt:   normalizedSystemPrompt(params.SystemPrompt),
		SourcePath:     spec.SourcePath,
	}, nil
}

func buildDesiredScheduleStates(spec AgentSpec) ([]desiredScheduleState, error) {
	params, err := buildManagedScheduleParams(spec)
	if err != nil {
		return nil, err
	}
	states := make([]desiredScheduleState, 0, len(params))
	for _, param := range params {
		payloadJSON, err := cpstore.MarshalEnvelope("schedule_payload", param.Payload)
		if err != nil {
			return nil, fmt.Errorf("encode schedule payload for %q/%q/%q: %w", param.NamespaceID, param.AgentID, param.ScheduleID, err)
		}
		states = append(states, desiredScheduleState{
			NamespaceID: param.NamespaceID,
			AgentID:     param.AgentID,
			ScheduleID:  param.ScheduleID,
			CronExpr:    param.CronExpr,
			TimeZone:    param.TimeZone,
			PayloadJSON: payloadJSON,
			Enabled:     param.Enabled,
			SourcePath:  spec.SourcePath,
		})
	}
	return states, nil
}

func managedAgentConfig(spec AgentSpec, normalizedTools []agent.ConfiguredTool) (map[string]any, error) {
	config := map[string]any{
		"managed_by":  filesystemConfigManager,
		"source_path": spec.SourcePath,
		"description": spec.Description,
		"config":      spec.Config,
	}
	if len(spec.Capabilities) > 0 {
		config["capabilities"] = spec.Capabilities
	}
	if len(normalizedTools) > 0 {
		config["tools"] = normalizedTools
	}
	if hasAgentFilesystemSettings(spec.Runtime.Filesystem) {
		config["filesystem"] = managedAgentFilesystemConfig{
			Kind: agentFilesystemKind(spec.Runtime.Filesystem),
		}
	}
	if hasAgentMemorySettings(spec.Memory) {
		config["memory"] = spec.Memory
	}
	if hasAgentMountSettings(spec.Mounts) {
		mounts, err := managedAgentMounts(spec)
		if err != nil {
			return nil, err
		}
		config["mounts"] = mounts
	}
	if hasAgentNetworkSettings(spec.Network) {
		network, err := managedAgentNetworkSettings(spec)
		if err != nil {
			return nil, err
		}
		config["network"] = *network
	}
	if hasAgentHTTPSettings(spec.HTTP) {
		headers, err := managedAgentHTTPHeaders(spec)
		if err != nil {
			return nil, err
		}
		config["http"] = managedAgentHTTPConfig{Headers: headers}
	}
	if spec.Runtime.OutputFileThresholdBytes > 0 {
		config["output_file_threshold_bytes"] = spec.Runtime.OutputFileThresholdBytes
	}
	return config, nil
}

func normalizeScheduleTimeZone(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.UTC.String(), nil
	}
	location, err := time.LoadLocation(raw)
	if err != nil {
		return "", fmt.Errorf("load schedule timezone %q: %w", raw, err)
	}
	return location.String(), nil
}

func diffNamespaceState(existing cpstore.Namespace, desired desiredNamespaceState) []FieldChange {
	var changes []FieldChange
	if existing.Name != desired.Name {
		changes = append(changes, FieldChange{
			Field:  "name",
			Before: existing.Name,
			After:  desired.Name,
		})
	}
	if existing.LimitsJSON != desired.LimitsJSON {
		changes = append(changes, FieldChange{
			Field:  "limits",
			Before: RenderEnvelope(existing.LimitsJSON),
			After:  RenderEnvelope(desired.LimitsJSON),
		})
	}
	return changes
}

func diffAgentState(existing cpstore.RunnableAgent, desired desiredAgentState) []FieldChange {
	var changes []FieldChange
	appendChange := func(field, before, after string) {
		if before == after {
			return
		}
		changes = append(changes, FieldChange{Field: field, Before: before, After: after})
	}

	appendChange("name", existing.Name, desired.Name)
	appendChange("role", string(existing.Role), string(desired.Role))
	appendChange("desired_state", string(existing.DesiredState), string(desired.DesiredState))
	appendChange("root_path", existing.RootPath, desired.RootPath)
	appendChange("model_provider", existing.ModelProvider, desired.ModelProvider)
	appendChange("model_name", existing.ModelName, desired.ModelName)
	appendChange("model_base_url", existing.ModelBaseURL, desired.ModelBaseURL)
	appendChange(
		"global_network_enabled",
		fmt.Sprintf("%t", GlobalNetworkEnabledFromConfigJSON(existing.ConfigJSON)),
		fmt.Sprintf("%t", GlobalNetworkEnabledFromConfigJSON(desired.ConfigJSON)),
	)
	appendChange("preserve_state", fmt.Sprintf("%t", existing.PreserveState), fmt.Sprintf("%t", desired.PreserveState))
	appendChange("max_steps", fmt.Sprintf("%d", existing.MaxSteps), fmt.Sprintf("%d", desired.MaxSteps))
	appendChange("step_timeout", existing.StepTimeout.String(), desired.StepTimeout.String())
	appendChange("max_output_bytes", fmt.Sprintf("%d", existing.MaxOutputBytes), fmt.Sprintf("%d", desired.MaxOutputBytes))
	appendChange("lease_duration", existing.LeaseDuration.String(), desired.LeaseDuration.String())
	appendChange("retry_delay", existing.RetryDelay.String(), desired.RetryDelay.String())
	appendChange("max_attempts", fmt.Sprintf("%d", existing.MaxAttempts), fmt.Sprintf("%d", desired.MaxAttempts))
	appendChange("config", RenderEnvelope(existing.ConfigJSON), RenderEnvelope(desired.ConfigJSON))
	appendChange("system_prompt", existing.SystemPrompt, desired.SystemPrompt)
	return changes
}

func diffScheduleState(existing cpstore.ScheduleRecord, desired desiredScheduleState) []FieldChange {
	var changes []FieldChange
	appendChange := func(field, before, after string) {
		if before == after {
			return
		}
		changes = append(changes, FieldChange{Field: field, Before: before, After: after})
	}

	appendChange("agent_id", existing.AgentID, desired.AgentID)
	appendChange("cron_expr", existing.CronExpr, desired.CronExpr)
	appendChange("timezone", existing.TimeZone, desired.TimeZone)
	appendChange("enabled", fmt.Sprintf("%t", existing.Enabled), fmt.Sprintf("%t", desired.Enabled))
	appendChange("payload", RenderEnvelope(existing.PayloadJSON), RenderEnvelope(desired.PayloadJSON))
	return changes
}

func joinPlanKey(parts ...string) string {
	return strings.Join(parts, "\x00")
}

func formatStringList(values []string) string {
	if len(values) == 0 {
		return "[]"
	}
	return "[" + strings.Join(values, ", ") + "]"
}
