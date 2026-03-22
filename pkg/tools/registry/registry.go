package registry

import (
	"fmt"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/richardartoul/swarmd/pkg/sh/interp"
	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
)

// RegistrationOptions describes how a tool participates in the shared registry.
type RegistrationOptions struct {
	BuiltIn      bool
	RequiredEnv  []string
	RequiredHosts []interp.HostMatcher
}

type toolRegistration struct {
	plugin        toolscore.ToolPlugin
	builtIn       bool
	requiredEnv   []string
	requiredHosts []interp.HostMatcher
}

// ResolvedToolBinding is one fully constructed tool definition and handler pair.
type ResolvedToolBinding struct {
	Definition toolscore.ToolDefinition
	Handler    toolscore.ToolHandler
}

var (
	registryMu       sync.RWMutex
	toolRegistry     = make(map[string]toolRegistration)
	builtInToolOrder []string
)

// MustRegister registers one tool and panics if the registration is invalid.
func MustRegister(plugin toolscore.ToolPlugin, opts RegistrationOptions) {
	if err := Register(plugin, opts); err != nil {
		panic(err)
	}
}

// Register registers one tool.
func Register(plugin toolscore.ToolPlugin, opts RegistrationOptions) error {
	if plugin == nil {
		return fmt.Errorf("tool plugin must not be nil")
	}
	definition := plugin.Definition()
	name := strings.TrimSpace(definition.Name)
	if name == "" {
		return fmt.Errorf("tool plugin name must not be empty")
	}
	if !toolscommon.IsSafeIdentifier(name) {
		return fmt.Errorf("tool plugin name %q must contain only letters, numbers, dots, underscores, or hyphens", name)
	}
	definition.Name = name
	definition.NetworkScope = definition.NetworkScope.Normalized()
	requiredEnv := normalizedRequiredEnv(opts.RequiredEnv)
	requiredHosts, err := normalizedRequiredHosts(opts.RequiredHosts)
	if err != nil {
		return fmt.Errorf("tool plugin %q required hosts invalid: %w", name, err)
	}
	if err := validateRegistrationNetworkScope(definition, requiredHosts); err != nil {
		return fmt.Errorf("tool plugin %q invalid: %w", name, err)
	}

	registryMu.Lock()
	defer registryMu.Unlock()
	if _, ok := toolRegistry[name]; ok {
		return fmt.Errorf("tool plugin %q is already registered", name)
	}
	toolRegistry[name] = toolRegistration{
		plugin: wrappedToolPlugin{
			definition: definition,
			next:       plugin,
		},
		builtIn:       opts.BuiltIn,
		requiredEnv:   requiredEnv,
		requiredHosts: requiredHosts,
	}
	if opts.BuiltIn {
		builtInToolOrder = append(builtInToolOrder, name)
	}
	return nil
}

// RegisterBuiltIn registers one built-in tool.
func RegisterBuiltIn(plugin toolscore.ToolPlugin) error {
	return Register(plugin, RegistrationOptions{BuiltIn: true})
}

// BuiltInToolNames returns the built-in tool catalog in stable order.
func BuiltInToolNames() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return slices.Clone(builtInToolOrder)
}

// RequiredEnvForTool returns the environment variables required by one tool.
func RequiredEnvForTool(name string) []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	registration, ok := toolRegistry[name]
	if !ok || len(registration.requiredEnv) == 0 {
		return nil
	}
	return slices.Clone(registration.requiredEnv)
}

// RequiredHostsForTool returns the host matchers required by one tool's scoped policy.
func RequiredHostsForTool(name string) []interp.HostMatcher {
	registryMu.RLock()
	defer registryMu.RUnlock()
	registration, ok := toolRegistry[name]
	if !ok || len(registration.requiredHosts) == 0 {
		return nil
	}
	return slices.Clone(registration.requiredHosts)
}

// NormalizeConfiguredTools validates and normalizes explicit custom tool references.
func NormalizeConfiguredTools(tools []toolscore.ConfiguredTool) ([]toolscore.ConfiguredTool, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	seen := make(map[string]int, len(tools))
	normalized := make([]toolscore.ConfiguredTool, 0, len(tools))
	for index, tool := range tools {
		if !tool.EnabledValue() {
			continue
		}
		tool = tool.Clone()
		tool.ID = strings.TrimSpace(tool.ID)
		if tool.ID == "" {
			return nil, fmt.Errorf("tools[%d].id must not be empty", index)
		}
		if !toolscommon.IsSafeIdentifier(tool.ID) {
			return nil, fmt.Errorf("tools[%d].id %q must contain only letters, numbers, dots, underscores, or hyphens", index, tool.ID)
		}
		registration, ok := lookupToolRegistration(tool.ID)
		if !ok {
			return nil, fmt.Errorf("tools[%d].id %q is not supported", index, tool.ID)
		}
		if registration.builtIn {
			return nil, fmt.Errorf("tools[%d].id %q is built-in and must not be listed in tools", index, tool.ID)
		}
		if previous, ok := seen[tool.ID]; ok {
			return nil, fmt.Errorf("duplicate tool %q in tools[%d] and tools[%d]", tool.ID, previous, index)
		}
		seen[tool.ID] = index
		normalized = append(normalized, tool)
	}
	return normalized, nil
}

// ResolveToolDefinitions returns the built-in tool surface plus any explicitly configured custom tools.
func ResolveToolDefinitions(tools []toolscore.ConfiguredTool, globalReachableHosts []interp.HostMatcher) ([]toolscore.ToolDefinition, error) {
	bindings, err := ResolveToolBindings(tools, globalReachableHosts)
	if err != nil {
		return nil, err
	}
	definitions := make([]toolscore.ToolDefinition, 0, len(bindings))
	for _, binding := range bindings {
		definitions = append(definitions, binding.Definition)
	}
	return definitions, nil
}

// ResolveActionSchema returns a stable, JSON-marshalable schema document for the tool surface exposed to an agent.
func ResolveActionSchema(tools []toolscore.ConfiguredTool, globalReachableHosts []interp.HostMatcher) (map[string]any, error) {
	definitions, err := ResolveToolDefinitions(tools, globalReachableHosts)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"version": 1,
		"tools":   definitions,
		"finish": map[string]any{
			"name":        "finish",
			"description": "Finish is a runtime-owned completion path, not a model-facing tool.",
		},
	}, nil
}

// ResolveToolBindings returns fully constructed built-in and explicit tool bindings.
func ResolveToolBindings(tools []toolscore.ConfiguredTool, globalReachableHosts []interp.HostMatcher) ([]ResolvedToolBinding, error) {
	explicitTools, err := resolveCustomToolBindings(tools, globalReachableHosts)
	if err != nil {
		return nil, err
	}
	bindings := make([]ResolvedToolBinding, 0, len(builtInToolOrder)+len(explicitTools))
	for _, name := range BuiltInToolNames() {
		registration, ok := lookupToolRegistration(name)
		if !ok {
			return nil, fmt.Errorf("built-in tool %q is not registered", name)
		}
		definition := registration.plugin.Definition()
		if !toolIsAvailable(definition.NetworkScope, registration.requiredHosts, globalReachableHosts) {
			continue
		}
		handler, err := registration.plugin.NewHandler(toolscore.ConfiguredTool{ID: name})
		if err != nil {
			return nil, fmt.Errorf("build built-in tool %q: %w", name, err)
		}
		bindings = append(bindings, ResolvedToolBinding{
			Definition: definition,
			Handler:    handler,
		})
	}
	bindings = append(bindings, explicitTools...)
	return bindings, nil
}

func resolveCustomToolBindings(tools []toolscore.ConfiguredTool, globalReachableHosts []interp.HostMatcher) ([]ResolvedToolBinding, error) {
	normalized, err := NormalizeConfiguredTools(tools)
	if err != nil {
		return nil, err
	}
	bindings := make([]ResolvedToolBinding, 0, len(normalized))
	for _, tool := range normalized {
		registration, ok := lookupToolRegistration(tool.ID)
		if !ok {
			return nil, fmt.Errorf("tool %q is not registered", tool.ID)
		}
		definition := registration.plugin.Definition()
		if !toolIsAvailable(definition.NetworkScope, registration.requiredHosts, globalReachableHosts) {
			return nil, unavailableToolNetworkScopeError(tool.ID, definition.NetworkScope)
		}
		handler, err := registration.plugin.NewHandler(tool)
		if err != nil {
			return nil, fmt.Errorf("build tool %q: %w", tool.ID, err)
		}
		bindings = append(bindings, ResolvedToolBinding{
			Definition: definition,
			Handler:    handler,
		})
	}
	return bindings, nil
}

func lookupToolRegistration(name string) (toolRegistration, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	registration, ok := toolRegistry[name]
	return registration, ok
}

func normalizedRequiredEnv(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func normalizedRequiredHosts(values []interp.HostMatcher) ([]interp.HostMatcher, error) {
	if len(values) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(values))
	normalized := make([]interp.HostMatcher, 0, len(values))
	for _, value := range values {
		matcher := interp.HostMatcher{
			Glob:  strings.TrimSpace(value.Glob),
			Regex: strings.TrimSpace(value.Regex),
		}
		hasGlob := matcher.Glob != ""
		hasRegex := matcher.Regex != ""
		if hasGlob == hasRegex {
			return nil, fmt.Errorf("each host matcher must set exactly one of glob or regex")
		}
		if hasGlob {
			if err := interp.ValidateHostPattern(matcher.Glob); err != nil {
				return nil, fmt.Errorf("glob %q invalid: %w", matcher.Glob, err)
			}
			if _, err := path.Match(matcher.Glob, "example.test"); err != nil {
				return nil, fmt.Errorf("glob %q invalid: %w", matcher.Glob, err)
			}
		} else {
			if _, err := interp.NormalizeHostRegexPattern(matcher.Regex); err != nil {
				return nil, fmt.Errorf("regex %q invalid: %w", matcher.Regex, err)
			}
		}
		key := matcher.Glob
		if key != "" {
			key = "glob:" + key
		} else {
			key = "regex:" + matcher.Regex
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, matcher)
	}
	if len(normalized) == 0 {
		return nil, nil
	}
	return normalized, nil
}

func validateRegistrationNetworkScope(definition toolscore.ToolDefinition, requiredHosts []interp.HostMatcher) error {
	switch definition.NetworkScope.Normalized() {
	case toolscore.ToolNetworkScopeNone:
		if len(requiredHosts) > 0 {
			return fmt.Errorf(`network_scope "none" must not declare required hosts`)
		}
	case toolscore.ToolNetworkScopeGlobal:
		if len(requiredHosts) > 0 {
			return fmt.Errorf(`network_scope "global" must not declare required hosts`)
		}
	case toolscore.ToolNetworkScopeScoped:
		if len(requiredHosts) == 0 {
			return fmt.Errorf(`network_scope "scoped" must declare at least one required host`)
		}
	default:
		return fmt.Errorf("unsupported network_scope %q", definition.NetworkScope)
	}
	return nil
}

func toolIsAvailable(scope toolscore.ToolNetworkScope, requiredHosts, globalReachableHosts []interp.HostMatcher) bool {
	switch scope.Normalized() {
	case toolscore.ToolNetworkScopeNone:
		return true
	case toolscore.ToolNetworkScopeGlobal:
		return len(globalReachableHosts) > 0
	case toolscore.ToolNetworkScopeScoped:
		return len(requiredHosts) > 0
	default:
		return false
	}
}

func unavailableToolNetworkScopeError(toolID string, scope toolscore.ToolNetworkScope) error {
	switch scope.Normalized() {
	case toolscore.ToolNetworkScopeGlobal:
		return fmt.Errorf("tool %q requires network.reachable_hosts to be configured", toolID)
	case toolscore.ToolNetworkScopeScoped:
		return fmt.Errorf("tool %q has no scoped host policy", toolID)
	default:
		return fmt.Errorf("tool %q is not available", toolID)
	}
}

type wrappedToolPlugin struct {
	definition toolscore.ToolDefinition
	next       toolscore.ToolPlugin
}

func (p wrappedToolPlugin) Definition() toolscore.ToolDefinition {
	return p.definition
}

func (p wrappedToolPlugin) NewHandler(config toolscore.ConfiguredTool) (toolscore.ToolHandler, error) {
	return p.next.NewHandler(config)
}
