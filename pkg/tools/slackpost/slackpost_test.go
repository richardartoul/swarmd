package slackpost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/richardartoul/swarmd/pkg/agent"
	"github.com/richardartoul/swarmd/pkg/server"
	"github.com/richardartoul/swarmd/pkg/sh/interp"
)

var registerTestsOnce sync.Once

func registerTestTool(t *testing.T) {
	t.Helper()
	registerTestsOnce.Do(Register)
}

func TestSlackClientPostMessage(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.postMessage" {
			t.Fatalf("request path = %q, want %q", r.URL.Path, "/chat.postMessage")
		}
		if r.Method != http.MethodPost {
			t.Fatalf("request method = %q, want %q", r.Method, http.MethodPost)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer xoxp-test" {
			t.Fatalf("Authorization header = %q, want bearer token", got)
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		if got := body["channel"]; got != "C123" {
			t.Fatalf("request channel = %#v, want %q", got, "C123")
		}
		if got := body["text"]; got != "hello from test" {
			t.Fatalf("request text = %#v, want %q", got, "hello from test")
		}
		if got := body["thread_ts"]; got != "1700.000001" {
			t.Fatalf("request thread_ts = %#v, want %q", got, "1700.000001")
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"channel": "C123",
			"ts":      "1700.000002",
			"message": map[string]any{
				"ts":        "1700.000002",
				"thread_ts": "1700.000001",
			},
		})
	}))
	defer server.Close()

	client := newTestSlackClient(t, server)
	result, err := client.PostMessage(context.Background(), SlackPostMessageParams{
		Channel:  "C123",
		Text:     "hello from test",
		ThreadTS: "1700.000001",
	})
	if err != nil {
		t.Fatalf("PostMessage() error = %v", err)
	}
	if result.Channel != "C123" || result.TS != "1700.000002" || result.ThreadTS != "1700.000001" {
		t.Fatalf("PostMessage() result = %#v, want channel/ts/thread_ts", result)
	}
}

func TestSlackClientFormatsAPIErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":       false,
			"error":    "missing_scope",
			"needed":   "chat:write",
			"provided": "users:read",
		})
	}))
	defer server.Close()

	client := newTestSlackClient(t, server)
	_, err := client.PostMessage(context.Background(), SlackPostMessageParams{
		Channel: "C123",
		Text:    "hello",
	})
	if err == nil {
		t.Fatal("PostMessage() error = nil, want API error")
	}
	if !strings.Contains(err.Error(), "missing_scope") || !strings.Contains(err.Error(), "chat:write") {
		t.Fatalf("PostMessage() error = %v, want formatted slack scope error", err)
	}
}

func TestSlackPostToolUsesDefaultChannelFromToolConfig(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.postMessage" {
			t.Fatalf("request path = %q, want %q", r.URL.Path, "/chat.postMessage")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		if got := body["channel"]; got != "C-default" {
			t.Fatalf("request channel = %#v, want %q", got, "C-default")
		}
		if got := body["text"]; got != "hello world" {
			t.Fatalf("request text = %#v, want %q", got, "hello world")
		}
		if _, ok := body["thread_ts"]; ok {
			t.Fatalf("request body unexpectedly included thread_ts: %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"channel": "C-default",
			"ts":      "1700.000001",
			"message": map[string]any{
				"ts": "1700.000001",
			},
		})
	}))
	defer server.Close()

	step := runSlackPostToolStep(t, map[string]any{
		"text": "hello world",
	}, map[string]any{
		"default_channel": "C-default",
	}, true, map[string]string{
		SlackUserTokenEnvVar:  "xoxp-test",
		slackAPIBaseURLEnvVar: server.URL,
	})
	if step.Status != agent.StepStatusOK {
		t.Fatalf("step.Status = %q, want %q (error=%q)", step.Status, agent.StepStatusOK, step.Error)
	}
	if strings.TrimSpace(step.Error) != "" {
		t.Fatalf("step.Error = %q, want empty", step.Error)
	}

	var result SlackPostMessageResult
	if err := json.Unmarshal([]byte(step.ActionOutput), &result); err != nil {
		t.Fatalf("Unmarshal(step.ActionOutput) error = %v", err)
	}
	if result.Channel != "C-default" || result.TS != "1700.000001" || result.ThreadTS != "1700.000001" {
		t.Fatalf("tool result = %#v, want default channel and ts/thread_ts", result)
	}
}

func TestSlackPostToolDoesNotRequireGlobalReachableHosts(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	_, err := agent.New(agent.Config{
		Root:          t.TempDir(),
		NetworkDialer: interp.OSNetworkDialer{},
		ConfiguredTools: []agent.ConfiguredTool{{
			ID: toolName,
		}},
		Driver: agent.DriverFunc(func(_ context.Context, _ agent.Request) (agent.Decision, error) {
			return agent.Decision{Finish: &agent.FinishAction{Value: "ok"}}, nil
		}),
	})
	if err != nil {
		t.Fatalf("agent.New() error = %v, want scoped tool to auto-allow required hosts", err)
	}
}

func TestSlackPostToolReportsUsageErrors(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	step := runSlackPostToolStep(t, map[string]any{
		"channel": "C123",
	}, nil, true, map[string]string{
		SlackUserTokenEnvVar: "xoxp-test",
	})
	if step.Status != agent.StepStatusPolicyError {
		t.Fatalf("step.Status = %q, want %q", step.Status, agent.StepStatusPolicyError)
	}
	if !strings.Contains(step.Error, "text must not be empty") {
		t.Fatalf("step.Error = %q, want missing text error", step.Error)
	}
}

func runSlackPostToolStep(t *testing.T, input any, config map[string]any, networkEnabled bool, env map[string]string) agent.Step {
	t.Helper()

	inputJSON, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("json.Marshal(input) error = %v", err)
	}
	var decisionCount int
	var networkDialer interp.NetworkDialer
	var globalReachableHosts []interp.HostMatcher
	if networkEnabled {
		networkDialer = interp.OSNetworkDialer{}
		globalReachableHosts = []interp.HostMatcher{{Glob: "*"}}
	}
	runtime, err := agent.New(agent.Config{
		Root:                 t.TempDir(),
		NetworkDialer:        networkDialer,
		GlobalReachableHosts: globalReachableHosts,
		ConfiguredTools: []agent.ConfiguredTool{{
			ID:     toolName,
			Config: config,
		}},
		ToolRuntimeData: testRuntime{env: env},
		Driver: agent.DriverFunc(func(_ context.Context, _ agent.Request) (agent.Decision, error) {
			if decisionCount == 0 {
				decisionCount++
				return agent.Decision{
					Tool: &agent.ToolAction{
						Name:  toolName,
						Kind:  agent.ToolKindFunction,
						Input: string(inputJSON),
					},
				}, nil
			}
			return agent.Decision{Finish: &agent.FinishAction{Value: "ok"}}, nil
		}),
		SystemPrompt: "test prompt",
	})
	if err != nil {
		t.Fatalf("agent.New() error = %v", err)
	}

	result, err := runtime.HandleTrigger(context.Background(), agent.Trigger{
		ID:   "trigger-test",
		Kind: "test",
	})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("len(result.Steps) = %d, want 1", len(result.Steps))
	}
	return result.Steps[0]
}

func newTestSlackClient(t *testing.T, server *httptest.Server) *SlackClient {
	t.Helper()

	client, err := NewSlackClient(SlackClientConfig{
		Token:   "xoxp-test",
		BaseURL: server.URL,
		Client:  server.Client(),
	})
	if err != nil {
		t.Fatalf("NewSlackClient() error = %v", err)
	}
	return client
}

type testRuntime struct {
	env map[string]string
}

func (r testRuntime) NamespaceID() string {
	return "default"
}

func (r testRuntime) AgentID() string {
	return "worker"
}

func (r testRuntime) LookupEnv(name string) string {
	if r.env == nil {
		return ""
	}
	return r.env[name]
}

func (r testRuntime) Logger() server.ToolLogger {
	return nil
}
