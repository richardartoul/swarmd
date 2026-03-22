package slackreplies

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestSlackClientListThreadRepliesPaginatesAndFiltersRootMessage(t *testing.T) {
	t.Parallel()

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/conversations.replies" {
			t.Fatalf("request path = %q, want %q", r.URL.Path, "/conversations.replies")
		}
		if got := r.Header.Get("Authorization"); got != "Bearer xoxp-test" {
			t.Fatalf("Authorization header = %q, want bearer token", got)
		}
		requestCount++

		switch requestCount {
		case 1:
			assertSlackRepliesQuery(t, r.URL.Query(), map[string]string{
				"channel":   "C123",
				"ts":        "1700.000001",
				"oldest":    "1700.000002",
				"inclusive": "false",
				"limit":     "200",
			})
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"messages": []map[string]any{
					{"ts": "1700.000001", "text": "root"},
					{"ts": "1700.000002", "thread_ts": "1700.000001", "text": "too old"},
					{"ts": "1700.000003", "thread_ts": "1700.000001", "text": "first new reply", "user": "U123"},
				},
				"response_metadata": map[string]any{
					"next_cursor": "cursor-2",
				},
			})
		case 2:
			assertSlackRepliesQuery(t, r.URL.Query(), map[string]string{
				"channel":   "C123",
				"ts":        "1700.000001",
				"oldest":    "1700.000002",
				"inclusive": "false",
				"limit":     "200",
				"cursor":    "cursor-2",
			})
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"messages": []map[string]any{
					{"ts": "1700.000004", "thread_ts": "1700.000001", "text": "second new reply", "bot_id": "B123"},
				},
				"response_metadata": map[string]any{
					"next_cursor": "",
				},
			})
		default:
			t.Fatalf("received unexpected request #%d", requestCount)
		}
	}))
	defer server.Close()

	client := newTestSlackClient(t, server)
	result, err := client.ListThreadReplies(context.Background(), SlackListThreadRepliesParams{
		Channel:  "C123",
		ThreadTS: "1700.000001",
		AfterTS:  "1700.000002",
	})
	if err != nil {
		t.Fatalf("ListThreadReplies() error = %v", err)
	}
	if len(result.Messages) != 2 {
		t.Fatalf("len(ListThreadReplies().Messages) = %d, want 2", len(result.Messages))
	}
	if result.Messages[0].Text != "first new reply" || result.Messages[1].Text != "second new reply" {
		t.Fatalf("ListThreadReplies() messages = %#v, want filtered replies", result.Messages)
	}
}

func TestSlackRepliesToolReturnsMessagesAfterCursor(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/conversations.replies" {
			t.Fatalf("request path = %q, want %q", r.URL.Path, "/conversations.replies")
		}
		if got := r.URL.Query().Get("channel"); got != "C-default" {
			t.Fatalf("query channel = %q, want %q", got, "C-default")
		}
		if got := r.URL.Query().Get("ts"); got != "1700.000001" {
			t.Fatalf("query ts = %q, want %q", got, "1700.000001")
		}
		if got := r.URL.Query().Get("oldest"); got != "1700.000002" {
			t.Fatalf("query oldest = %q, want %q", got, "1700.000002")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"messages": []map[string]any{
				{"ts": "1700.000001", "text": "root"},
				{"ts": "1700.000002", "thread_ts": "1700.000001", "text": "old reply"},
				{"ts": "1700.000003", "thread_ts": "1700.000001", "text": "new reply", "user": "U123"},
			},
		})
	}))
	defer server.Close()

	step := runSlackRepliesToolStep(t, map[string]any{
		"thread_ts": "1700.000001",
		"after_ts":  "1700.000002",
	}, map[string]any{
		"default_channel": "C-default",
	}, true, map[string]string{
		SlackUserTokenEnvVar:  "xoxp-test",
		slackAPIBaseURLEnvVar: server.URL,
	})
	if step.Status != agent.StepStatusOK {
		t.Fatalf("step.Status = %q, want %q (error=%q)", step.Status, agent.StepStatusOK, step.Error)
	}
	var result SlackListThreadRepliesResult
	if err := json.Unmarshal([]byte(step.ActionOutput), &result); err != nil {
		t.Fatalf("Unmarshal(step.ActionOutput) error = %v", err)
	}
	if len(result.Messages) != 1 || result.Messages[0].Text != "new reply" {
		t.Fatalf("tool replies = %#v, want one filtered reply", result.Messages)
	}
}

func runSlackRepliesToolStep(t *testing.T, input any, config map[string]any, networkEnabled bool, env map[string]string) agent.Step {
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

func assertSlackRepliesQuery(t *testing.T, got url.Values, want map[string]string) {
	t.Helper()
	for key, expected := range want {
		if got.Get(key) != expected {
			t.Fatalf("query[%q] = %q, want %q", key, got.Get(key), expected)
		}
	}
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
