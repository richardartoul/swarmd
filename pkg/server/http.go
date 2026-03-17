package server

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/richardartoul/swarmd/pkg/sh/interp"
)

type managedAgentHTTPConfig struct {
	Headers []managedAgentHTTPHeader `json:"headers,omitempty"`
}

type managedAgentHTTPHeader struct {
	Name    string                    `json:"name,omitempty"`
	Value   *string                   `json:"value,omitempty"`
	EnvVar  string                    `json:"env_var,omitempty"`
	Domains []managedAgentHostMatcher `json:"domains,omitempty"`
}

func validateAgentHTTP(spec AgentSpec) error {
	for i, header := range spec.HTTP.Headers {
		name := strings.TrimSpace(header.Name)
		if name == "" {
			return fmt.Errorf("http.headers[%d].name must not be empty", i)
		}
		hasValue := header.Value != nil
		hasEnvVar := strings.TrimSpace(header.EnvVar) != ""
		if httpHeaderValueSelectionCount(hasValue, hasEnvVar) != 1 {
			return fmt.Errorf("http.headers[%d] must set exactly one of value or env_var", i)
		}
		if err := validateAgentHostMatchers(fmt.Sprintf("http.headers[%d].domains", i), header.Domains, false); err != nil {
			return err
		}
	}
	return nil
}

func hasAgentHTTPSettings(httpSpec AgentHTTPSpec) bool {
	return len(httpSpec.Headers) > 0
}

func managedAgentHTTPHeaders(spec AgentSpec) ([]managedAgentHTTPHeader, error) {
	if len(spec.HTTP.Headers) == 0 {
		return nil, nil
	}
	headers := make([]managedAgentHTTPHeader, 0, len(spec.HTTP.Headers))
	for _, header := range spec.HTTP.Headers {
		domains := make([]managedAgentHostMatcher, 0, len(header.Domains))
		for _, domain := range header.Domains {
			domains = append(domains, managedAgentHostMatcher{
				Glob:  strings.TrimSpace(domain.Glob),
				Regex: strings.TrimSpace(domain.Regex),
			})
		}
		var value *string
		if header.Value != nil {
			copied := *header.Value
			value = &copied
		}
		headers = append(headers, managedAgentHTTPHeader{
			Name:    strings.TrimSpace(header.Name),
			Value:   value,
			EnvVar:  strings.TrimSpace(header.EnvVar),
			Domains: domains,
		})
	}
	return headers, nil
}

func resolveManagedHTTPHeaderRules(headers []managedAgentHTTPHeader, lookupEnv func(string) string) ([]interp.HTTPHeaderRule, error) {
	if len(headers) == 0 {
		return nil, nil
	}
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	rules := make([]interp.HTTPHeaderRule, 0, len(headers))
	for _, header := range headers {
		value, err := managedHTTPHeaderValue(header, lookupEnv)
		if err != nil {
			return nil, err
		}
		domains := make([]interp.HTTPDomainMatcher, 0, len(header.Domains))
		for _, domain := range header.Domains {
			domains = append(domains, interp.HTTPDomainMatcher{
				Glob:  strings.TrimSpace(domain.Glob),
				Regex: strings.TrimSpace(domain.Regex),
			})
		}
		rules = append(rules, interp.HTTPHeaderRule{
			Name:    strings.TrimSpace(header.Name),
			Value:   value,
			Domains: domains,
		})
	}
	return rules, nil
}

func managedHTTPHeaderValue(header managedAgentHTTPHeader, lookupEnv func(string) string) (string, error) {
	if header.Value != nil {
		return *header.Value, nil
	}
	envVar := strings.TrimSpace(header.EnvVar)
	value := lookupEnv(envVar)
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("environment variable %q for HTTP header %q is empty or not set", envVar, header.Name)
	}
	return value, nil
}

func httpHeaderEnvVars(headers []managedAgentHTTPHeader) []string {
	if len(headers) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(headers))
	var envVars []string
	for _, header := range headers {
		envVar := strings.TrimSpace(header.EnvVar)
		if envVar == "" {
			continue
		}
		if _, ok := seen[envVar]; ok {
			continue
		}
		seen[envVar] = struct{}{}
		envVars = append(envVars, envVar)
	}
	sort.Strings(envVars)
	return envVars
}

func httpHeaderPromptGuidance(headers []managedAgentHTTPHeader) string {
	if len(headers) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("The runtime automatically injects HTTP headers on matching outbound requests.\n")
	builder.WriteString("You usually do not need to add these headers manually with curl.\n")
	builder.WriteString("Configured header rules:\n")
	for _, header := range headers {
		builder.WriteString("- ")
		builder.WriteString(strings.TrimSpace(header.Name))
		if scopes := formatHTTPHeaderDomains(header.Domains); scopes != "" {
			builder.WriteString(" (")
			builder.WriteString(scopes)
			builder.WriteString(")")
		}
		builder.WriteString("\n")
	}
	return strings.TrimSpace(builder.String())
}

func formatHTTPHeaderDomains(domains []managedAgentHostMatcher) string {
	if len(domains) == 0 {
		return "all domains"
	}
	return formatManagedHostMatchers(domains)
}

func httpHeaderValueSelectionCount(values ...bool) int {
	count := 0
	for _, value := range values {
		if value {
			count++
		}
	}
	return count
}
