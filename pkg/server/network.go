package server

import (
	"fmt"
	"path"
	"strings"

	"github.com/richardartoul/swarmd/pkg/sh/interp"
)

const legacyCapabilityAllowNetwork = "allow_network"

type managedAgentNetworkConfig struct {
	ReachableHosts []managedAgentHostMatcher `json:"reachable_hosts,omitempty"`
}

type managedAgentHostMatcher struct {
	Glob  string `json:"glob,omitempty"`
	Regex string `json:"regex,omitempty"`
}

type managedAgentHTTPDomainMatcher = managedAgentHostMatcher

func validateAgentCapabilities(spec AgentSpec) error {
	if spec.Capabilities == nil {
		return nil
	}
	if _, ok := spec.Capabilities[legacyCapabilityAllowNetwork]; ok {
		return fmt.Errorf("capabilities.allow_network is no longer supported; use network.reachable_hosts instead")
	}
	return nil
}

func validateAgentNetwork(spec AgentSpec) error {
	if spec.Network == nil {
		return nil
	}
	return validateAgentHostMatchers("network.reachable_hosts", spec.Network.ReachableHosts, true)
}

func validateAgentHostMatchers(field string, matchers []AgentHostMatcherSpec, requireNonEmpty bool) error {
	if requireNonEmpty && len(matchers) == 0 {
		return fmt.Errorf("%s must not be empty", field)
	}
	for i, matcher := range matchers {
		hasGlob := strings.TrimSpace(matcher.Glob) != ""
		hasRegex := strings.TrimSpace(matcher.Regex) != ""
		if httpHeaderValueSelectionCount(hasGlob, hasRegex) != 1 {
			return fmt.Errorf("%s[%d] must set exactly one of glob or regex", field, i)
		}
		if hasGlob {
			glob := strings.TrimSpace(matcher.Glob)
			if err := interp.ValidateHostPattern(glob); err != nil {
				return fmt.Errorf("%s[%d].glob invalid: %w", field, i, err)
			}
			if _, err := path.Match(glob, "example.test"); err != nil {
				return fmt.Errorf("%s[%d].glob invalid: %w", field, i, err)
			}
			continue
		}
		if _, err := interp.NormalizeHostRegexPattern(matcher.Regex); err != nil {
			return fmt.Errorf("%s[%d].regex invalid: %w", field, i, err)
		}
	}
	return nil
}

func agentNetworkEnabled(spec *AgentNetworkSpec) bool {
	return spec != nil && len(spec.ReachableHosts) > 0
}

func hasAgentNetworkSettings(spec *AgentNetworkSpec) bool {
	return agentNetworkEnabled(spec)
}

func managedAgentNetworkSettings(spec AgentSpec) (*managedAgentNetworkConfig, error) {
	if !hasAgentNetworkSettings(spec.Network) {
		return nil, nil
	}
	reachableHosts := make([]managedAgentHostMatcher, 0, len(spec.Network.ReachableHosts))
	for _, matcher := range spec.Network.ReachableHosts {
		reachableHosts = append(reachableHosts, managedAgentHostMatcher{
			Glob:  strings.TrimSpace(matcher.Glob),
			Regex: strings.TrimSpace(matcher.Regex),
		})
	}
	return &managedAgentNetworkConfig{ReachableHosts: reachableHosts}, nil
}

func agentNetworkHostMatchers(spec *AgentNetworkSpec) []interp.HostMatcher {
	if spec == nil || len(spec.ReachableHosts) == 0 {
		return nil
	}
	matchers := make([]interp.HostMatcher, 0, len(spec.ReachableHosts))
	for _, matcher := range spec.ReachableHosts {
		matchers = append(matchers, interp.HostMatcher{
			Glob:  strings.TrimSpace(matcher.Glob),
			Regex: strings.TrimSpace(matcher.Regex),
		})
	}
	return matchers
}

func resolveManagedNetworkHostMatchers(network managedAgentNetworkConfig) []interp.HostMatcher {
	if len(network.ReachableHosts) == 0 {
		return nil
	}
	matchers := make([]interp.HostMatcher, 0, len(network.ReachableHosts))
	for _, matcher := range network.ReachableHosts {
		matchers = append(matchers, interp.HostMatcher{
			Glob:  strings.TrimSpace(matcher.Glob),
			Regex: strings.TrimSpace(matcher.Regex),
		})
	}
	return matchers
}

func networkPromptGuidance(network managedAgentNetworkConfig) string {
	if len(network.ReachableHosts) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("Outbound network access is limited to matching hosts.\n")
	builder.WriteString("Configured reachable hosts:\n")
	for _, matcher := range network.ReachableHosts {
		builder.WriteString("- ")
		builder.WriteString(formatManagedHostMatcher(matcher))
		builder.WriteString("\n")
	}
	return strings.TrimSpace(builder.String())
}

func formatManagedHostMatchers(matchers []managedAgentHostMatcher) string {
	if len(matchers) == 0 {
		return ""
	}
	formatted := make([]string, 0, len(matchers))
	for _, matcher := range matchers {
		formatted = append(formatted, formatManagedHostMatcher(matcher))
	}
	return strings.Join(formatted, ", ")
}

func formatManagedHostMatcher(matcher managedAgentHostMatcher) string {
	switch {
	case strings.TrimSpace(matcher.Glob) != "":
		return "glob:" + strings.TrimSpace(matcher.Glob)
	case strings.TrimSpace(matcher.Regex) != "":
		return "regex:" + strings.TrimSpace(matcher.Regex)
	default:
		return "invalid"
	}
}
