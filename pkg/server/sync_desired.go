// sync_desired.go translates loaded agent specs into the desired store
// state (agent params, schedules, embedded runtime config) that the sync
// plan diffs against existing records.

package server

import (
	"fmt"
	"strings"
	"time"

	"github.com/richardartoul/swarmd/pkg/agent"
	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
	"github.com/robfig/cron/v3"
)

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
