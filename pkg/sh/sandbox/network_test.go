// See LICENSE for licensing information

package sandbox_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/richardartoul/swarmd/pkg/sh/interp"
	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
	"github.com/richardartoul/swarmd/pkg/sh/syntax"
)

func TestNewRunnerWithConfigDefaultsToRejectAllNetworkDialer(t *testing.T) {
	policy, err := sandbox.NewFS(t.TempDir())
	if err != nil {
		t.Fatalf("sandbox.NewFS error: %v", err)
	}

	called := false
	runner, err := sandbox.NewRunnerWithConfig(policy, sandbox.RunnerConfig{
		Stdout: io.Discard,
		Stderr: io.Discard,
		ExecHandlers: []func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc{
			func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
				return func(ctx context.Context, args []string) error {
					if args[0] != "dial" {
						return next(ctx, args)
					}
					called = true
					hc := interp.HandlerCtx(ctx)
					_, err := hc.NetworkDialer.DialContext(ctx, "tcp", "127.0.0.1:1")
					if !errors.Is(err, interp.ErrNetworkDialRejected) {
						t.Fatalf("DialContext error = %v, want %v", err, interp.ErrNetworkDialRejected)
					}
					return nil
				}
			},
		},
	})
	if err != nil {
		t.Fatalf("sandbox.NewRunnerWithConfig error: %v", err)
	}

	if err := runTestProgram(t, runner, "dial"); err != nil {
		t.Fatalf("Runner.Run error: %v", err)
	}
	if !called {
		t.Fatal("expected exec middleware to run")
	}
}

func TestNewRunnerWithConfigUsesInjectedNetworkDialer(t *testing.T) {
	policy, err := sandbox.NewFS(t.TempDir())
	if err != nil {
		t.Fatalf("sandbox.NewFS error: %v", err)
	}

	fakeDialer := &recordingDialer{}
	runner, err := sandbox.NewRunnerWithConfig(policy, sandbox.RunnerConfig{
		Stdout:        io.Discard,
		Stderr:        io.Discard,
		NetworkDialer: fakeDialer,
		HTTPHeaders: []interp.HTTPHeaderRule{{
			Name:  "Authorization",
			Value: "Bearer secret",
		}},
		ExecHandlers: []func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc{
			func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
				return func(ctx context.Context, args []string) error {
					if args[0] != "dial" {
						return next(ctx, args)
					}
					hc := interp.HandlerCtx(ctx)
					conn, err := hc.NetworkDialer.DialContext(ctx, "tcp", "sandbox.test:443")
					if err != nil {
						return err
					}
					return conn.Close()
				}
			},
		},
	})
	if err != nil {
		t.Fatalf("sandbox.NewRunnerWithConfig error: %v", err)
	}

	if err := runTestProgram(t, runner, "dial"); err != nil {
		t.Fatalf("Runner.Run error: %v", err)
	}
	if len(fakeDialer.calls) != 1 {
		t.Fatalf("DialContext call count = %d, want 1", len(fakeDialer.calls))
	}
	if fakeDialer.calls[0].network != "tcp" || fakeDialer.calls[0].address != "sandbox.test:443" {
		t.Fatalf("DialContext call = %#v, want tcp/sandbox.test:443", fakeDialer.calls[0])
	}
}

func TestNewRunnerWithConfigControlsNetworkCommandDiscovery(t *testing.T) {
	t.Parallel()

	policy, err := sandbox.NewFS(t.TempDir())
	if err != nil {
		t.Fatalf("sandbox.NewFS error: %v", err)
	}

	var disabledStdout bytes.Buffer
	disabledRunner, err := sandbox.NewRunnerWithConfig(policy, sandbox.RunnerConfig{
		Stdout: &disabledStdout,
		Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("sandbox.NewRunnerWithConfig() disabled error = %v", err)
	}
	if err := runTestProgram(t, disabledRunner, "command -v curl"); !errors.Is(err, interp.ExitStatus(1)) {
		t.Fatalf("disabled discovery error = %v, want exit status 1", err)
	}
	if got := disabledStdout.String(); got != "" {
		t.Fatalf("disabled discovery stdout = %q, want empty", got)
	}

	fakeDialer := &recordingDialer{}
	var enabledStdout bytes.Buffer
	enabledRunner, err := sandbox.NewRunnerWithConfig(policy, sandbox.RunnerConfig{
		Stdout:         &enabledStdout,
		Stderr:         io.Discard,
		NetworkDialer:  fakeDialer,
		NetworkEnabled: true,
	})
	if err != nil {
		t.Fatalf("sandbox.NewRunnerWithConfig() enabled error = %v", err)
	}
	if err := runTestProgram(t, enabledRunner, "command -v curl"); err != nil {
		t.Fatalf("enabled discovery error = %v", err)
	}
	if got := enabledStdout.String(); got != "curl\n" {
		t.Fatalf("enabled discovery stdout = %q, want %q", got, "curl\n")
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
