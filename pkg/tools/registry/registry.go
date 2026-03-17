package registry

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
)

// RegistrationOptions describes how a tool participates in the shared registry.
type RegistrationOptions struct {
	BuiltIn     bool
	RequiredEnv []string
}

type toolRegistration struct {
	plugin      toolscore.ToolPlugin
	builtIn     bool
	requiredEnv []string
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
	requiredEnv := normalizedRequiredEnv(opts.RequiredEnv)

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
		builtIn:     opts.BuiltIn,
		requiredEnv: requiredEnv,
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
func ResolveToolDefinitions(tools []toolscore.ConfiguredTool, networkEnabled bool) ([]toolscore.ToolDefinition, error) {
	bindings, err := ResolveToolBindings(tools, networkEnabled)
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
func ResolveActionSchema(tools []toolscore.ConfiguredTool, networkEnabled bool) (map[string]any, error) {
	definitions, err := ResolveToolDefinitions(tools, networkEnabled)
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
func ResolveToolBindings(tools []toolscore.ConfiguredTool, networkEnabled bool) ([]ResolvedToolBinding, error) {
	explicitTools, err := resolveCustomToolBindings(tools, networkEnabled)
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
		if definition.RequiresNetwork && !networkEnabled {
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

func resolveCustomToolBindings(tools []toolscore.ConfiguredTool, networkEnabled bool) ([]ResolvedToolBinding, error) {
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
		if definition.RequiresNetwork && !networkEnabled {
			return nil, fmt.Errorf("tool %q requires network but network.reachable_hosts is not configured", tool.ID)
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
