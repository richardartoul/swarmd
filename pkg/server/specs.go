package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/richardartoul/swarmd/pkg/agent"
	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"
)

const (
	filesystemConfigManager   = "filesystem"
	supportedAgentSpecVersion = 1
)

type AgentSpec struct {
	SourcePath   string              `yaml:"-"`
	NamespaceID  string              `yaml:"-"`
	Version      *int                `yaml:"version"`
	AgentID      string              `yaml:"agent_id,omitempty"`
	Name         string              `yaml:"name,omitempty"`
	Description  string              `yaml:"description,omitempty"`
	Model        AgentModelSpec      `yaml:"model"`
	Prompt       string              `yaml:"prompt"`
	RootPath     string              `yaml:"root_path,omitempty"`
	Memory       AgentMemorySpec     `yaml:"memory,omitempty"`
	Mounts       []AgentMountSpec    `yaml:"mounts,omitempty"`
	HTTP         AgentHTTPSpec       `yaml:"http,omitempty"`
	Network      *AgentNetworkSpec   `yaml:"network,omitempty"`
	Capabilities map[string]any      `yaml:"capabilities,omitempty"`
	Tools        []AgentToolSpec     `yaml:"tools,omitempty"`
	Runtime      AgentRuntimeSpec    `yaml:"runtime,omitempty"`
	Schedules    []AgentScheduleSpec `yaml:"schedules,omitempty"`
	Config       map[string]any      `yaml:"config,omitempty"`
}

type AgentModelSpec struct {
	Provider string `yaml:"provider,omitempty"`
	Name     string `yaml:"name"`
	BaseURL  string `yaml:"base_url,omitempty"`
}

type AgentMemorySpec struct {
	Disable        bool   `yaml:"disable,omitempty" json:"disable,omitempty"`
	PromptOverride string `yaml:"prompt_override,omitempty" json:"prompt_override,omitempty"`
}

type AgentRuntimeSpec struct {
	DesiredState             string              `yaml:"desired_state,omitempty"`
	Filesystem               AgentFilesystemSpec `yaml:"filesystem,omitempty"`
	PreserveState            bool                `yaml:"preserve_state,omitempty"`
	MaxSteps                 int                 `yaml:"max_steps,omitempty"`
	StepTimeout              string              `yaml:"step_timeout,omitempty"`
	MaxOutputBytes           int                 `yaml:"max_output_bytes,omitempty"`
	OutputFileThresholdBytes int                 `yaml:"output_file_threshold_bytes,omitempty"`
	LeaseDuration            string              `yaml:"lease_duration,omitempty"`
	RetryDelay               string              `yaml:"retry_delay,omitempty"`
	MaxAttempts              int                 `yaml:"max_attempts,omitempty"`
}

type AgentFilesystemSpec struct {
	Kind string `yaml:"kind,omitempty" json:"kind,omitempty"`
}

type AgentHTTPSpec struct {
	Headers []AgentHTTPHeaderSpec `yaml:"headers,omitempty" json:"headers,omitempty"`
}

type AgentNetworkSpec struct {
	ReachableHosts []AgentHostMatcherSpec `yaml:"reachable_hosts,omitempty" json:"reachable_hosts,omitempty"`
}

type AgentHTTPHeaderSpec struct {
	Name    string                 `yaml:"name,omitempty" json:"name,omitempty"`
	Value   *string                `yaml:"value,omitempty" json:"value,omitempty"`
	EnvVar  string                 `yaml:"env_var,omitempty" json:"env_var,omitempty"`
	Domains []AgentHostMatcherSpec `yaml:"domains,omitempty" json:"domains,omitempty"`
}

type AgentHostMatcherSpec struct {
	Glob  string `yaml:"glob,omitempty" json:"glob,omitempty"`
	Regex string `yaml:"regex,omitempty" json:"regex,omitempty"`
}

type AgentHTTPDomainMatcherSpec = AgentHostMatcherSpec

type AgentToolSpec struct {
	ID      string         `yaml:"id,omitempty" json:"id,omitempty"`
	Enabled *bool          `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Config  map[string]any `yaml:"config,omitempty" json:"config,omitempty"`
}

func (s *AgentToolSpec) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		var id string
		if err := value.Decode(&id); err != nil {
			return err
		}
		*s = AgentToolSpec{ID: strings.TrimSpace(id)}
		return nil
	case yaml.MappingNode:
		type rawAgentToolSpec AgentToolSpec
		var decoded rawAgentToolSpec
		if err := value.Decode(&decoded); err != nil {
			return err
		}
		*s = AgentToolSpec(decoded)
		return nil
	default:
		return fmt.Errorf("must be a string or object")
	}
}

type AgentScheduleSpec struct {
	ID       string `yaml:"id,omitempty"`
	CronExpr string `yaml:"cron,omitempty"`
	TimeZone string `yaml:"timezone,omitempty"`
	Enabled  *bool  `yaml:"enabled,omitempty"`
	Payload  any    `yaml:"payload,omitempty"`
}

type SyncSummary struct {
	NamespacesCreated int
	NamespacesUpdated int
	AgentsCreated     int
	AgentsUpdated     int
	AgentsDeleted     int
	SchedulesCreated  int
	SchedulesDeleted  int
}

func LoadAgentSpecs(configRoot string) ([]AgentSpec, error) {
	configRoot = filepath.Clean(strings.TrimSpace(configRoot))
	if configRoot == "" {
		return nil, fmt.Errorf("config root must not be empty")
	}
	info, err := os.Stat(configRoot)
	if err != nil {
		return nil, fmt.Errorf("stat config root %q: %w", configRoot, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("config root %q is not a directory", configRoot)
	}

	baseDir := filepath.Join(configRoot, "namespaces")
	if _, err := os.Stat(baseDir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []AgentSpec{}, nil
		}
		return nil, fmt.Errorf("stat namespace config dir %q: %w", baseDir, err)
	}

	var specs []AgentSpec
	seen := map[string]string{}
	seenScheduleIDs := map[string]string{}
	err = filepath.WalkDir(baseDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if ext := strings.ToLower(filepath.Ext(entry.Name())); ext != ".yaml" && ext != ".yml" {
			return nil
		}

		rel, err := filepath.Rel(configRoot, path)
		if err != nil {
			return err
		}
		parts := splitPath(rel)
		if len(parts) != 4 || parts[0] != "namespaces" || parts[2] != "agents" {
			return fmt.Errorf("agent spec %q must live at namespaces/<namespace>/agents/<file>.yaml", path)
		}

		spec, err := loadAgentSpecFile(path)
		if err != nil {
			return err
		}
		spec.SourcePath = path
		spec.NamespaceID = parts[1]
		spec.AgentID = strings.TrimSpace(spec.AgentID)
		if spec.AgentID == "" {
			spec.AgentID = strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		}
		if err := validateAgentSpec(spec); err != nil {
			return fmt.Errorf("agent spec %q invalid: %w", path, err)
		}
		key := spec.NamespaceID + ":" + spec.AgentID
		if existingPath, ok := seen[key]; ok {
			return fmt.Errorf("duplicate agent spec for %q in %q and %q", key, existingPath, path)
		}
		seen[key] = path
		for i, schedule := range spec.Schedules {
			scheduleID := toolscommon.FirstNonEmptyString(schedule.ID, derivedScheduleID(spec.AgentID, i))
			scheduleKey := spec.NamespaceID + ":" + scheduleID
			if existingPath, ok := seenScheduleIDs[scheduleKey]; ok {
				return fmt.Errorf("duplicate schedule id %q for namespace %q in %q and %q", scheduleID, spec.NamespaceID, existingPath, path)
			}
			seenScheduleIDs[scheduleKey] = path
		}
		specs = append(specs, spec)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(specs, func(i, j int) bool {
		if specs[i].NamespaceID != specs[j].NamespaceID {
			return specs[i].NamespaceID < specs[j].NamespaceID
		}
		return specs[i].AgentID < specs[j].AgentID
	})
	return specs, nil
}

func SyncSpecsFromConfigRoot(ctx context.Context, store *cpstore.Store, configRoot, defaultRootBase string) (SyncSummary, error) {
	if store == nil {
		return SyncSummary{}, fmt.Errorf("sync specs requires a store")
	}
	specs, err := LoadAgentSpecs(configRoot)
	if err != nil {
		return SyncSummary{}, err
	}
	if err := validateUniqueAgentRoots(configRoot, defaultRootBase, specs); err != nil {
		return SyncSummary{}, err
	}

	summary := SyncSummary{}
	desiredByNamespace := make(map[string]map[string]AgentSpec)
	for _, spec := range specs {
		if _, ok := desiredByNamespace[spec.NamespaceID]; !ok {
			desiredByNamespace[spec.NamespaceID] = make(map[string]AgentSpec)
		}
		desiredByNamespace[spec.NamespaceID][spec.AgentID] = spec
	}

	namespaceIDs := sortedNamespaceIDs(desiredByNamespace)
	for _, namespaceID := range namespaceIDs {
		result, err := store.PutNamespace(ctx, cpstore.CreateNamespaceParams{
			ID:   namespaceID,
			Name: namespaceID,
		})
		if err != nil {
			return SyncSummary{}, err
		}
		if result.Created {
			summary.NamespacesCreated++
		}
		if result.Updated {
			summary.NamespacesUpdated++
		}
	}

	for _, spec := range specs {
		agentParams, err := buildManagedAgentParams(configRoot, defaultRootBase, spec)
		if err != nil {
			return SyncSummary{}, err
		}
		putResult, err := store.PutAgent(ctx, agentParams)
		if err != nil {
			return SyncSummary{}, fmt.Errorf("put agent %q/%q: %w", spec.NamespaceID, spec.AgentID, err)
		}
		if putResult.Created {
			summary.AgentsCreated++
		}
		if putResult.Updated {
			summary.AgentsUpdated++
		}

		deletedSchedules, err := store.DeleteSchedulesByAgent(ctx, spec.NamespaceID, spec.AgentID)
		if err != nil {
			return SyncSummary{}, err
		}
		summary.SchedulesDeleted += deletedSchedules
		scheduleParams, err := buildManagedScheduleParams(spec)
		if err != nil {
			return SyncSummary{}, err
		}
		for _, scheduleParam := range scheduleParams {
			if _, err := store.CreateSchedule(ctx, scheduleParam); err != nil {
				return SyncSummary{}, fmt.Errorf("create schedule %q for %q/%q: %w", scheduleParam.ScheduleID, spec.NamespaceID, spec.AgentID, err)
			}
			summary.SchedulesCreated++
		}
	}

	existingNamespaces, err := store.ListNamespaces(ctx)
	if err != nil {
		return SyncSummary{}, err
	}
	for _, namespace := range existingNamespaces {
		snapshot, err := store.SnapshotNamespace(ctx, namespace.ID)
		if err != nil {
			return SyncSummary{}, err
		}
		desiredAgents := desiredByNamespace[namespace.ID]
		for _, existingAgent := range snapshot.Agents {
			if _, ok := desiredAgents[existingAgent.ID]; ok {
				continue
			}
			if err := store.DeleteAgent(ctx, namespace.ID, existingAgent.ID); err != nil {
				if errors.Is(err, cpstore.ErrNotFound) {
					continue
				}
				return SyncSummary{}, err
			}
			summary.AgentsDeleted++
		}
	}
	return summary, nil
}

func loadAgentSpecFile(path string) (AgentSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return AgentSpec{}, fmt.Errorf("read agent spec %q: %w", path, err)
	}
	var spec AgentSpec
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&spec); err != nil {
		return AgentSpec{}, fmt.Errorf("decode agent spec %q: %w", path, err)
	}
	return spec, nil
}

func validateAgentSpec(spec AgentSpec) error {
	if strings.TrimSpace(spec.NamespaceID) == "" {
		return fmt.Errorf("namespace id must not be empty")
	}
	if spec.Version == nil {
		return fmt.Errorf("version must be set")
	}
	if *spec.Version != supportedAgentSpecVersion {
		return fmt.Errorf("unsupported version %d, supported version is %d", *spec.Version, supportedAgentSpecVersion)
	}
	if err := validateAgentID(spec.AgentID); err != nil {
		return err
	}
	if err := validateModelProvider(spec.Model.Provider); err != nil {
		return err
	}
	if strings.TrimSpace(spec.Model.Name) == "" {
		return fmt.Errorf("model.name must not be empty")
	}
	if strings.TrimSpace(spec.Prompt) == "" {
		return fmt.Errorf("prompt must not be empty")
	}
	if spec.Memory.Disable && strings.TrimSpace(spec.Memory.PromptOverride) != "" {
		return fmt.Errorf("memory.prompt_override cannot be set when memory.disable is true")
	}
	if err := validateAgentFilesystem(spec.Runtime.Filesystem); err != nil {
		return err
	}
	if err := validateAgentCapabilities(spec); err != nil {
		return err
	}
	if err := validateAgentMounts(spec); err != nil {
		return err
	}
	if err := validateAgentHTTP(spec); err != nil {
		return err
	}
	if err := validateAgentNetwork(spec); err != nil {
		return err
	}
	normalizedTools, err := normalizeAgentTools(spec.Tools)
	if err != nil {
		return fmt.Errorf("tools invalid: %w", err)
	}
	if _, err := agent.ResolveToolDefinitions(normalizedTools, agentNetworkEnabled(spec.Network)); err != nil {
		return fmt.Errorf("tools invalid: %w", err)
	}
	if _, err := parseOptionalDuration(spec.Runtime.StepTimeout); err != nil {
		return fmt.Errorf("runtime.step_timeout invalid: %w", err)
	}
	if _, err := parseOptionalDuration(spec.Runtime.LeaseDuration); err != nil {
		return fmt.Errorf("runtime.lease_duration invalid: %w", err)
	}
	if _, err := parseOptionalDuration(spec.Runtime.RetryDelay); err != nil {
		return fmt.Errorf("runtime.retry_delay invalid: %w", err)
	}
	seenScheduleIDs := make(map[string]int, len(spec.Schedules))
	for i, schedule := range spec.Schedules {
		if strings.TrimSpace(schedule.CronExpr) == "" {
			return fmt.Errorf("schedules[%d].cron must not be empty", i)
		}
		if _, err := cron.ParseStandard(strings.TrimSpace(schedule.CronExpr)); err != nil {
			return fmt.Errorf("schedules[%d].cron invalid: %w", i, err)
		}
		if _, err := normalizeScheduleTimeZone(schedule.TimeZone); err != nil {
			return fmt.Errorf("schedules[%d].timezone invalid: %w", i, err)
		}
		scheduleID := toolscommon.FirstNonEmptyString(schedule.ID, derivedScheduleID(spec.AgentID, i))
		if previous, ok := seenScheduleIDs[scheduleID]; ok {
			return fmt.Errorf("duplicate schedule id %q in schedules[%d] and schedules[%d]", scheduleID, previous, i)
		}
		seenScheduleIDs[scheduleID] = i
	}
	return nil
}

func validateModelProvider(provider string) error {
	switch strings.TrimSpace(provider) {
	case "", "openai", "anthropic":
		return nil
	default:
		return fmt.Errorf(`model.provider must be empty, "openai", or "anthropic"`)
	}
}

func validateAgentID(agentID string) error {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return fmt.Errorf("agent id must not be empty")
	}
	if agentID == "." || agentID == ".." {
		return fmt.Errorf("agent id %q must not be %q", agentID, agentID)
	}
	if !toolscommon.IsSafeIdentifier(agentID) {
		return fmt.Errorf("agent id %q must contain only letters, numbers, dots, underscores, or hyphens", agentID)
	}
	return nil
}

func validateUniqueAgentRoots(configRoot, defaultRootBase string, specs []AgentSpec) error {
	seen := make(map[string]string, len(specs))
	for _, spec := range specs {
		if agentFilesystemKind(spec.Runtime.Filesystem) == managedAgentFilesystemKindMemory {
			continue
		}
		rootPath, err := resolveAgentRootPath(configRoot, defaultRootBase, spec)
		if err != nil {
			return fmt.Errorf("resolve root for %q/%q: %w", spec.NamespaceID, spec.AgentID, err)
		}
		agentRef := spec.NamespaceID + "/" + spec.AgentID
		if existing, ok := seen[rootPath]; ok {
			return fmt.Errorf("agents %q and %q resolve to the same root path %q", existing, agentRef, rootPath)
		}
		seen[rootPath] = agentRef
	}
	return nil
}

func resolveAgentRootPath(configRoot, defaultRootBase string, spec AgentSpec) (string, error) {
	if agentFilesystemKind(spec.Runtime.Filesystem) == managedAgentFilesystemKindMemory {
		return resolveLogicalMemoryRootPath(spec.RootPath)
	}
	if strings.TrimSpace(spec.RootPath) != "" {
		if filepath.IsAbs(spec.RootPath) {
			return filepath.Clean(spec.RootPath), nil
		}
		return filepath.Clean(filepath.Join(configRoot, spec.RootPath)), nil
	}
	if strings.TrimSpace(defaultRootBase) == "" {
		return "", fmt.Errorf("root_path is required when no default root base is configured")
	}
	return filepath.Join(defaultRootBase, spec.NamespaceID, spec.AgentID), nil
}

func validateAgentFilesystem(spec AgentFilesystemSpec) error {
	switch agentFilesystemKind(spec) {
	case managedAgentFilesystemKindDisk, managedAgentFilesystemKindMemory:
		return nil
	default:
		return fmt.Errorf(
			"runtime.filesystem.kind must be %q or %q",
			managedAgentFilesystemKindDisk,
			managedAgentFilesystemKindMemory,
		)
	}
}

func agentFilesystemKind(spec AgentFilesystemSpec) string {
	kind := strings.TrimSpace(spec.Kind)
	if kind == "" {
		return managedAgentFilesystemKindDisk
	}
	return kind
}

func hasAgentFilesystemSettings(spec AgentFilesystemSpec) bool {
	return strings.TrimSpace(spec.Kind) != ""
}

func resolveLogicalMemoryRootPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return string(os.PathSeparator), nil
	}
	if !isRootedAgentPath(raw) {
		raw = filepath.Join(string(os.PathSeparator), raw)
	}
	raw = filepath.Clean(raw)
	if !isRootedAgentPath(raw) {
		return "", fmt.Errorf("memory root_path %q must be absolute", raw)
	}
	return raw, nil
}

func isRootedAgentPath(path string) bool {
	if path == "" {
		return false
	}
	if filepath.IsAbs(path) {
		return true
	}
	return strings.HasPrefix(path, "/") || strings.HasPrefix(path, `\`)
}

func parseOptionalDuration(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	return time.ParseDuration(raw)
}

func desiredStateFromSpec(raw string) cpstore.AgentDesiredState {
	switch strings.TrimSpace(raw) {
	case string(cpstore.AgentDesiredStatePaused):
		return cpstore.AgentDesiredStatePaused
	case string(cpstore.AgentDesiredStateStopped):
		return cpstore.AgentDesiredStateStopped
	default:
		return cpstore.AgentDesiredStateRunning
	}
}

func capabilityBool(capabilities map[string]any, key string) bool {
	if capabilities == nil {
		return false
	}
	value, ok := capabilities[key]
	if !ok {
		return false
	}
	switch value := value.(type) {
	case bool:
		return value
	default:
		return false
	}
}

func normalizeAgentTools(tools []AgentToolSpec) ([]agent.ConfiguredTool, error) {
	configured := make([]agent.ConfiguredTool, 0, len(tools))
	for _, tool := range tools {
		configured = append(configured, agent.ConfiguredTool{
			ID:      strings.TrimSpace(tool.ID),
			Enabled: tool.Enabled,
			Config:  tool.Config,
		})
	}
	return agent.NormalizeConfiguredTools(configured)
}

func hasAgentMemorySettings(memory AgentMemorySpec) bool {
	return memory.Disable || strings.TrimSpace(memory.PromptOverride) != ""
}

func hasAgentMountSettings(mounts []AgentMountSpec) bool {
	return len(mounts) > 0
}

func defaultSchedulePayload(spec AgentSpec, index int) any {
	return map[string]any{
		"kind":     "scheduled_run",
		"agent_id": spec.AgentID,
		"schedule": derivedScheduleID(spec.AgentID, index),
		"source":   filepath.Base(spec.SourcePath),
	}
}

func derivedScheduleID(agentID string, index int) string {
	return agentID + "-schedule-" + strconv.Itoa(index+1)
}

func splitPath(path string) []string {
	path = filepath.ToSlash(path)
	return strings.Split(path, "/")
}

func sortedNamespaceIDs(desiredByNamespace map[string]map[string]AgentSpec) []string {
	namespaceIDs := make([]string, 0, len(desiredByNamespace))
	for namespaceID := range desiredByNamespace {
		namespaceIDs = append(namespaceIDs, namespaceID)
	}
	sort.Strings(namespaceIDs)
	return namespaceIDs
}
