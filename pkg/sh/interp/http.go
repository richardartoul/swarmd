package interp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

type HTTPHeaderRule struct {
	Name    string
	Value   string
	Domains []HTTPDomainMatcher
}

type HTTPClientOptions struct {
	ConnectTimeout  time.Duration
	FollowRedirects bool
}

type HTTPClientFactory interface {
	NewClient(opts HTTPClientOptions) *http.Client
}

type defaultHTTPClientFactory struct {
	dialer NetworkDialer
	rules  []compiledHTTPHeaderRule
}

type compiledHTTPHeaderRule struct {
	name    string
	value   string
	domains []compiledHostMatcher
}

type headerInjectingRoundTripper struct {
	next  http.RoundTripper
	rules []compiledHTTPHeaderRule
}

func NewHTTPClientFactory(dialer NetworkDialer, rules []HTTPHeaderRule) (HTTPClientFactory, error) {
	if dialer == nil {
		dialer = DefaultNetworkDialer()
	}
	compiledRules := make([]compiledHTTPHeaderRule, 0, len(rules))
	for _, rule := range rules {
		if strings.TrimSpace(rule.Name) == "" {
			return nil, fmt.Errorf("HTTP header rule name must not be empty")
		}
		domains := make([]compiledHostMatcher, 0, len(rule.Domains))
		for _, domain := range rule.Domains {
			switch {
			case strings.TrimSpace(domain.Glob) != "":
				matcher, err := compileHostGlobMatcher(domain.Glob)
				if err != nil {
					return nil, fmt.Errorf("HTTP header %q glob %q invalid: %w", rule.Name, domain.Glob, err)
				}
				domains = append(domains, matcher)
			case strings.TrimSpace(domain.Regex) != "":
				matcher, err := compileHostRegexMatcher(domain.Regex)
				if err != nil {
					return nil, fmt.Errorf("HTTP header %q regex %q invalid: %w", rule.Name, domain.Regex, err)
				}
				domains = append(domains, matcher)
			default:
				return nil, fmt.Errorf("HTTP header %q matcher must define glob or regex", rule.Name)
			}
		}
		compiledRules = append(compiledRules, compiledHTTPHeaderRule{
			name:    strings.TrimSpace(rule.Name),
			value:   rule.Value,
			domains: domains,
		})
	}
	return &defaultHTTPClientFactory{
		dialer: dialer,
		rules:  compiledRules,
	}, nil
}

func (f *defaultHTTPClientFactory) NewClient(opts HTTPClientOptions) *http.Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if opts.ConnectTimeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, opts.ConnectTimeout)
				defer cancel()
			}
			return f.dialer.DialContext(ctx, network, address)
		},
	}
	var roundTripper http.RoundTripper = transport
	if len(f.rules) > 0 {
		roundTripper = headerInjectingRoundTripper{
			next:  transport,
			rules: f.rules,
		}
	}
	client := &http.Client{Transport: roundTripper}
	if !opts.FollowRedirects {
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}
	return client
}

func (rt headerInjectingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	clone.Host = req.Host
	host := strings.ToLower(strings.TrimSpace(req.URL.Hostname()))
	for _, rule := range rt.rules {
		if !rule.matches(host) {
			continue
		}
		if strings.EqualFold(rule.name, "Host") {
			clone.Host = rule.value
			continue
		}
		clone.Header.Set(rule.name, rule.value)
	}
	return rt.next.RoundTrip(clone)
}

func (rule compiledHTTPHeaderRule) matches(host string) bool {
	if len(rule.domains) == 0 {
		return true
	}
	return hostMatchesAny(rule.domains, host)
}
