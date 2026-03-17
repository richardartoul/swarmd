// See LICENSE for licensing information

package interp

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// HostMatcher matches one outbound host via either glob or regex.
type HostMatcher struct {
	Glob  string
	Regex string
}

// HTTPDomainMatcher is kept as an alias for existing HTTP header rule call sites.
type HTTPDomainMatcher = HostMatcher

type compiledHostMatcher interface {
	Matches(host string) bool
}

type compiledHostGlobMatcher string

func (m compiledHostGlobMatcher) Matches(host string) bool {
	matched, err := path.Match(string(m), host)
	return err == nil && matched
}

type compiledHostRegexMatcher struct {
	pattern *regexp.Regexp
}

func (m compiledHostRegexMatcher) Matches(host string) bool {
	return m.pattern.MatchString(host)
}

func ValidateHostPattern(pattern string) error {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return fmt.Errorf("must not be empty")
	}
	pattern = strings.TrimPrefix(pattern, "^")
	pattern = strings.TrimSuffix(pattern, "$")
	if pattern == "" {
		return fmt.Errorf("must not be empty")
	}
	if strings.Contains(pattern, "://") || strings.Contains(pattern, `:\/\/`) {
		return fmt.Errorf("must match hostnames only; protocol prefixes like http:// or https:// are not allowed")
	}
	return nil
}

// ValidateHTTPHostPattern is kept for backward-compatible call sites.
func ValidateHTTPHostPattern(pattern string) error {
	return ValidateHostPattern(pattern)
}

func NormalizeHostRegexPattern(pattern string) (string, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return "", fmt.Errorf("must not be empty")
	}
	if err := ValidateHostPattern(pattern); err != nil {
		return "", err
	}
	pattern = strings.TrimPrefix(pattern, "^")
	pattern = strings.TrimSuffix(pattern, "$")
	if pattern == "" {
		return "", fmt.Errorf("must not be empty")
	}
	anchored := "^(?:" + pattern + ")$"
	if _, err := regexp.Compile(anchored); err != nil {
		return "", err
	}
	return anchored, nil
}

func compileHostGlobMatcher(glob string) (compiledHostMatcher, error) {
	glob = strings.ToLower(strings.TrimSpace(glob))
	if err := ValidateHostPattern(glob); err != nil {
		return nil, err
	}
	if _, err := path.Match(glob, "example.test"); err != nil {
		return nil, err
	}
	return compiledHostGlobMatcher(glob), nil
}

func compileHostRegexMatcher(regex string) (compiledHostMatcher, error) {
	pattern, err := NormalizeHostRegexPattern(regex)
	if err != nil {
		return nil, err
	}
	compiled, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return nil, err
	}
	return compiledHostRegexMatcher{pattern: compiled}, nil
}

func compileHostMatcher(matcher HostMatcher) (compiledHostMatcher, error) {
	switch {
	case strings.TrimSpace(matcher.Glob) != "":
		return compileHostGlobMatcher(matcher.Glob)
	case strings.TrimSpace(matcher.Regex) != "":
		return compileHostRegexMatcher(matcher.Regex)
	default:
		return nil, fmt.Errorf("must define glob or regex")
	}
}

func compileHostMatchers(matchers []HostMatcher) ([]compiledHostMatcher, error) {
	compiled := make([]compiledHostMatcher, 0, len(matchers))
	for _, matcher := range matchers {
		entry, err := compileHostMatcher(matcher)
		if err != nil {
			return nil, err
		}
		compiled = append(compiled, entry)
	}
	return compiled, nil
}

func hostMatchesAny(matchers []compiledHostMatcher, host string) bool {
	for _, matcher := range matchers {
		if matcher.Matches(host) {
			return true
		}
	}
	return false
}
