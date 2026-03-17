// See LICENSE for licensing information

package interp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
)

// ErrNetworkDialRejected is returned when network dialing is disabled.
var ErrNetworkDialRejected = errors.New("network dialing disabled")

// ErrNetworkHostRejected is returned when a dial target does not match the
// configured reachable host policy.
var ErrNetworkHostRejected = errors.New("network host not allowed")

// NetworkDialer is the network adapter used by interpreter-owned commands.
type NetworkDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// RejectAllNetworkDialer denies all outbound network dials.
type RejectAllNetworkDialer struct{}

var _ NetworkDialer = RejectAllNetworkDialer{}

// DefaultNetworkDialer returns the interpreter's default network adapter.
func DefaultNetworkDialer() NetworkDialer {
	return RejectAllNetworkDialer{}
}

func (RejectAllNetworkDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return nil, fmt.Errorf("dial %s %s: %w", network, address, ErrNetworkDialRejected)
}

// OSNetworkDialer dials the host network stack.
//
// It is not used by default; callers must opt into host networking explicitly.
type OSNetworkDialer struct {
	Dialer net.Dialer
}

var _ NetworkDialer = OSNetworkDialer{}

func (d OSNetworkDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d.Dialer.DialContext(ctx, network, address)
}

type allowlistNetworkDialer struct {
	base     NetworkDialer
	matchers []compiledHostMatcher
}

var _ NetworkDialer = allowlistNetworkDialer{}

// NewAllowlistNetworkDialer wraps another dialer with a host allowlist.
func NewAllowlistNetworkDialer(base NetworkDialer, matchers []HostMatcher) (NetworkDialer, error) {
	compiled, err := compileHostMatchers(matchers)
	if err != nil {
		return nil, err
	}
	if len(compiled) == 0 {
		return nil, fmt.Errorf("reachable hosts must not be empty")
	}
	if base == nil {
		base = DefaultNetworkDialer()
	}
	return allowlistNetworkDialer{
		base:     base,
		matchers: compiled,
	}, nil
}

func (d allowlistNetworkDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host := dialAddressHost(address)
	if strings.TrimSpace(host) == "" || !hostMatchesAny(d.matchers, host) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		return nil, fmt.Errorf("dial %s %s: host %q: %w", network, address, host, ErrNetworkHostRejected)
	}
	return d.base.DialContext(ctx, network, address)
}

func dialAddressHost(address string) string {
	address = strings.TrimSpace(address)
	if address == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(address); err == nil {
		return strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]"))
	}
	return strings.ToLower(strings.Trim(address, "[]"))
}
