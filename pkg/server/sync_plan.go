package server

import (
	"context"
	"fmt"
	"sort"
	"strings"

	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

// ChangeAction classifies one planned reconciliation change.
type ChangeAction string

const (
	ChangeActionCreate ChangeAction = "create"
	ChangeActionUpdate ChangeAction = "update"
	ChangeActionDelete ChangeAction = "delete"
)

// FieldChange is one before/after field difference in a planned update.
type FieldChange struct {
	Field  string
	Before string
	After  string
}

// AgentSpecSummary counts the entities declared by a set of agent specs.
type AgentSpecSummary struct {
	Namespaces int
	Agents     int
	Schedules  int
}

// SyncPlanSummary counts the changes a sync plan would apply.
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

// SyncPlan is a dry-run reconciliation of config-root specs against the
// store, grouped by entity.
type SyncPlan struct {
	Summary          SyncPlanSummary
	NamespaceChanges []NamespacePlanChange
	AgentChanges     []AgentPlanChange
	ScheduleChanges  []SchedulePlanChange
}

// NamespacePlanChange is one planned namespace create or update.
type NamespacePlanChange struct {
	Action      ChangeAction
	NamespaceID string
	Changes     []FieldChange
}

// AgentPlanChange is one planned agent create, update, or delete.
type AgentPlanChange struct {
	Action      ChangeAction
	NamespaceID string
	AgentID     string
	SourcePath  string
	RootPath    string
	ModelName   string
	Changes     []FieldChange
}

// SchedulePlanChange is one planned schedule create, update, or delete.
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

// SummarizeAgentSpecs counts namespaces, agents, and schedules in specs.
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

// HasChanges reports whether applying the plan would modify the store.
func (p SyncPlan) HasChanges() bool {
	return len(p.NamespaceChanges) > 0 || len(p.AgentChanges) > 0 || len(p.ScheduleChanges) > 0
}

// PlanSyncFromConfigRoot computes the dry-run reconciliation plan for the
// specs under configRoot without modifying the store.
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
