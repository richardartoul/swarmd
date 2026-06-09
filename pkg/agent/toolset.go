// See LICENSE for licensing information

package agent

import (
	"fmt"
	"slices"

	"github.com/richardartoul/swarmd/pkg/sh/interp"
	toolregistry "github.com/richardartoul/swarmd/pkg/tools/registry"
)

// toolset is the resolved structured-tool surface for one agent: the
// definitions exposed to the driver in catalog order, plus per-tool dispatch
// handlers and host-scoped HTTP client factories.
type toolset struct {
	definitions []ToolDefinition
	handlers    map[string]ToolHandler
	httpClients map[string]interp.HTTPClientFactory
}

// newToolset resolves the configured tools into a dispatchable toolset.
// Each tool receives an HTTP client factory scoped to its effective host
// policy: none, the agent's global reachable hosts, or the union of global
// hosts and the tool's registry-declared required hosts.
func newToolset(cfg Config, globalReachableHosts []interp.HostMatcher) (toolset, error) {
	bindings, err := resolveToolBindings(cfg.ConfiguredTools, globalReachableHosts)
	if err != nil {
		return toolset{}, fmt.Errorf("resolve agent tools: %w", err)
	}
	tools := toolset{
		definitions: make([]ToolDefinition, 0, len(bindings)),
		handlers:    make(map[string]ToolHandler, len(bindings)),
		httpClients: make(map[string]interp.HTTPClientFactory, len(bindings)),
	}
	for _, binding := range bindings {
		name := binding.Definition.Name
		tools.definitions = append(tools.definitions, binding.Definition)
		tools.handlers[name] = binding.Handler
		factory, err := newAgentHTTPClientFactory(
			cfg.NetworkDialer,
			effectiveToolReachableHosts(
				binding.Definition.NetworkScope,
				globalReachableHosts,
				toolregistry.RequiredHostsForTool(name),
			),
			cfg.HTTPHeaders,
		)
		if err != nil {
			return toolset{}, fmt.Errorf("create HTTP client factory for tool %q: %w", name, err)
		}
		tools.httpClients[name] = factory
	}
	return tools, nil
}

// Definitions returns a defensive copy of the driver-facing tool definitions.
func (t toolset) Definitions() []ToolDefinition {
	return slices.Clone(t.definitions)
}

// Handler returns the dispatch handler for a tool name.
func (t toolset) Handler(name string) (ToolHandler, bool) {
	handler, ok := t.handlers[name]
	return handler, ok
}

// HTTPClientFactory returns the host-scoped HTTP client factory for a tool
// name, or nil when the tool has no network access.
func (t toolset) HTTPClientFactory(name string) interp.HTTPClientFactory {
	return t.httpClients[name]
}

func effectiveToolReachableHosts(
	scope ToolNetworkScope,
	globalReachableHosts []interp.HostMatcher,
	requiredHosts []interp.HostMatcher,
) []interp.HostMatcher {
	switch scope.Normalized() {
	case ToolNetworkScopeNone:
		return nil
	case ToolNetworkScopeGlobal:
		return slices.Clone(globalReachableHosts)
	case ToolNetworkScopeScoped:
		return mergeHostMatchers(globalReachableHosts, requiredHosts)
	default:
		return nil
	}
}

func mergeHostMatchers(left, right []interp.HostMatcher) []interp.HostMatcher {
	if len(left) == 0 && len(right) == 0 {
		return nil
	}
	seen := make(map[interp.HostMatcher]struct{}, len(left)+len(right))
	merged := make([]interp.HostMatcher, 0, len(left)+len(right))
	appendMatchers := func(values []interp.HostMatcher) {
		for _, value := range values {
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			merged = append(merged, value)
		}
	}
	appendMatchers(left)
	appendMatchers(right)
	return merged
}
