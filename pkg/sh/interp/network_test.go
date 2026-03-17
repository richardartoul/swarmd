// See LICENSE for licensing information

package interp_test

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/richardartoul/swarmd/pkg/sh/interp"
	"github.com/richardartoul/swarmd/pkg/sh/syntax"
)

func TestDefaultNetworkDialerRejectsAll(t *testing.T) {
	called := false
	runner, err := interp.New(
		interp.StdIO(nil, io.Discard, io.Discard),
		interp.ExecHandlers(func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
			return func(ctx context.Context, args []string) error {
				if args[0] != "dial" {
					return next(ctx, args)
				}
				called = true
				hc := interp.HandlerCtx(ctx)
				if hc.NetworkDialer == nil {
					t.Fatal("HandlerContext.NetworkDialer should never be nil")
				}
				_, err := hc.NetworkDialer.DialContext(ctx, "tcp", "127.0.0.1:1")
				if !errors.Is(err, interp.ErrNetworkDialRejected) {
					t.Fatalf("DialContext error = %v, want %v", err, interp.ErrNetworkDialRejected)
				}
				return nil
			}
		}),
	)
	if err != nil {
		t.Fatalf("interp.New error: %v", err)
	}

	if err := runTestProgram(t, runner, "dial"); err != nil {
		t.Fatalf("Runner.Run error: %v", err)
	}
	if !called {
		t.Fatal("expected exec middleware to run")
	}
}

func TestNetworkOptionOverrideUsesInjectedDialer(t *testing.T) {
	fakeDialer := &recordingDialer{}
	runner, err := interp.New(
		interp.Network(fakeDialer),
		interp.StdIO(nil, io.Discard, io.Discard),
		interp.ExecHandlers(func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
			return func(ctx context.Context, args []string) error {
				if args[0] != "dial" {
					return next(ctx, args)
				}
				hc := interp.HandlerCtx(ctx)
				conn, err := hc.NetworkDialer.DialContext(ctx, "tcp", "example.com:443")
				if err != nil {
					return err
				}
				return conn.Close()
			}
		}),
	)
	if err != nil {
		t.Fatalf("interp.New error: %v", err)
	}

	if err := runTestProgram(t, runner, "dial"); err != nil {
		t.Fatalf("Runner.Run error: %v", err)
	}
	if len(fakeDialer.calls) != 1 {
		t.Fatalf("DialContext call count = %d, want 1", len(fakeDialer.calls))
	}
	if fakeDialer.calls[0].network != "tcp" || fakeDialer.calls[0].address != "example.com:443" {
		t.Fatalf("DialContext call = %#v, want tcp/example.com:443", fakeDialer.calls[0])
	}
}

func TestOSNetworkDialerCanDialLoopback(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen error: %v", err)
	}
	defer listener.Close()

	accepted := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			accepted <- err
			return
		}
		accepted <- conn.Close()
	}()

	dialer := interp.OSNetworkDialer{}
	conn, err := dialer.DialContext(context.Background(), "tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("DialContext error: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("conn.Close error: %v", err)
	}
	if err := <-accepted; err != nil {
		t.Fatalf("listener accept error: %v", err)
	}
}

func TestAllowlistNetworkDialerAllowsMatchingHost(t *testing.T) {
	base := &recordingDialer{}
	dialer, err := interp.NewAllowlistNetworkDialer(base, []interp.HostMatcher{{
		Regex: "payments-[a-z0-9-]+\\.example\\.com",
	}})
	if err != nil {
		t.Fatalf("NewAllowlistNetworkDialer() error = %v", err)
	}

	conn, err := dialer.DialContext(context.Background(), "tcp", "payments-abc.example.com:443")
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("conn.Close() error = %v", err)
	}
	if len(base.calls) != 1 {
		t.Fatalf("len(base.calls) = %d, want 1", len(base.calls))
	}
	if got := base.calls[0].address; got != "payments-abc.example.com:443" {
		t.Fatalf("base.calls[0].address = %q, want %q", got, "payments-abc.example.com:443")
	}
}

func TestAllowlistNetworkDialerRejectsNonMatchingHost(t *testing.T) {
	base := &recordingDialer{}
	dialer, err := interp.NewAllowlistNetworkDialer(base, []interp.HostMatcher{{
		Glob: "*.example.com",
	}})
	if err != nil {
		t.Fatalf("NewAllowlistNetworkDialer() error = %v", err)
	}

	_, err = dialer.DialContext(context.Background(), "tcp", "127.0.0.1:443")
	if !errors.Is(err, interp.ErrNetworkHostRejected) {
		t.Fatalf("DialContext() error = %v, want %v", err, interp.ErrNetworkHostRejected)
	}
	if len(base.calls) != 0 {
		t.Fatalf("len(base.calls) = %d, want 0 for rejected host", len(base.calls))
	}
}

type recordingDialer struct {
	calls []dialCall
}

type dialCall struct {
	network string
	address string
}

func (d *recordingDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.calls = append(d.calls, dialCall{network: network, address: address})
	first, second := net.Pipe()
	go second.Close()
	return first, nil
}

func runTestProgram(t *testing.T, runner *interp.Runner, src string) error {
	t.Helper()
	program, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	return runner.Run(context.Background(), program)
}
