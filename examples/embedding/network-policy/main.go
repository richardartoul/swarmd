package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"

	"github.com/richardartoul/swarmd/pkg/agent"
	"github.com/richardartoul/swarmd/pkg/sh/interp"
)

func main() {
	disabled, err := runDisabledDemo()
	if err != nil {
		panic(err)
	}

	blocked, err := runBlockedDemo()
	if err != nil {
		panic(err)
	}

	allowed, err := runAllowlistedDemo()
	if err != nil {
		panic(err)
	}

	fmt.Println("network disabled:")
	fmt.Printf("  step status: %s\n", disabled.Steps[0].Status)
	fmt.Printf("  exit status: %d\n", disabled.Steps[0].ExitStatus)
	fmt.Println()
	fmt.Println("network allowlist blocked:")
	fmt.Printf("  step status: %s\n", blocked.Steps[0].Status)
	fmt.Printf("  stderr: %s\n", strings.TrimSpace(blocked.Steps[0].Stderr))
	fmt.Println()
	fmt.Println("network allowlist allowed with injected header:")
	fmt.Printf("  step status: %s\n", allowed.Steps[0].Status)
	fmt.Printf("  stdout: %s\n", strings.TrimSpace(allowed.Steps[0].Stdout))
}

func runDisabledDemo() (agent.Result, error) {
	return runScriptedAgent(agent.Config{
		Driver: agent.DriverFunc(func(_ context.Context, req agent.Request) (agent.Decision, error) {
			if req.Step == 1 {
				// curl is hidden from the sandbox surface until networking is enabled.
				return agent.Decision{
					Tool: &agent.ToolAction{
						Name:  agent.ToolNameRunShell,
						Kind:  agent.ToolKindFunction,
						Input: `{"command":"command -v curl"}`,
					},
				}, nil
			}
			return agent.Decision{
				Finish: &agent.FinishAction{Value: "done"},
			}, nil
		}),
	})
}

func runBlockedDemo() (agent.Result, error) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "X-Demo-Header=%s", r.Header.Get("X-Demo-Header"))
	}))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return agent.Result{}, err
	}
	server.Listener = listener
	server.Start()
	defer server.Close()

	dialer, err := interp.NewAllowlistNetworkDialer(interp.OSNetworkDialer{}, []interp.HostMatcher{{
		Glob: "example.com",
	}})
	if err != nil {
		return agent.Result{}, err
	}

	return runScriptedAgent(agent.Config{
		NetworkDialer: dialer,
		Driver: agent.DriverFunc(func(_ context.Context, req agent.Request) (agent.Decision, error) {
			if req.Step == 1 {
				return agent.Decision{
					Tool: &agent.ToolAction{
						Name:  agent.ToolNameRunShell,
						Kind:  agent.ToolKindFunction,
						Input: fmt.Sprintf("{\"command\":%q}", "curl -s "+server.URL),
					},
				}, nil
			}
			return agent.Decision{
				Finish: &agent.FinishAction{Value: "done"},
			}, nil
		}),
	})
}

func runAllowlistedDemo() (agent.Result, error) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "X-Demo-Header=%s", r.Header.Get("X-Demo-Header"))
	}))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return agent.Result{}, err
	}
	server.Listener = listener
	server.Start()
	defer server.Close()

	dialer, err := interp.NewAllowlistNetworkDialer(interp.OSNetworkDialer{}, []interp.HostMatcher{{
		Glob: "127.0.0.1",
	}})
	if err != nil {
		return agent.Result{}, err
	}

	return runScriptedAgent(agent.Config{
		NetworkDialer: dialer,
		HTTPHeaders: []interp.HTTPHeaderRule{{
			Name:  "X-Demo-Header",
			Value: "from-agent",
			Domains: []interp.HTTPDomainMatcher{{
				Glob: "127.0.0.1",
			}},
		}},
		Driver: agent.DriverFunc(func(_ context.Context, req agent.Request) (agent.Decision, error) {
			if req.Step == 1 {
				return agent.Decision{
					Tool: &agent.ToolAction{
						Name:  agent.ToolNameRunShell,
						Kind:  agent.ToolKindFunction,
						Input: fmt.Sprintf("{\"command\":%q}", "curl -s "+server.URL),
					},
				}, nil
			}
			return agent.Decision{
				Finish: &agent.FinishAction{Value: "done"},
			}, nil
		}),
	})
}

func runScriptedAgent(cfg agent.Config) (result agent.Result, runErr error) {
	root, err := os.MkdirTemp("", "swarmd-network-policy")
	if err != nil {
		return agent.Result{}, err
	}
	defer os.RemoveAll(root)

	cfg.Root = root
	runtime, err := agent.New(cfg)
	if err != nil {
		return agent.Result{}, err
	}
	defer func() {
		if closeErr := runtime.Close(); runErr == nil {
			runErr = closeErr
		}
	}()
	result, runErr = runtime.HandleTrigger(context.Background(), agent.Trigger{Kind: "example"})
	return result, runErr
}
