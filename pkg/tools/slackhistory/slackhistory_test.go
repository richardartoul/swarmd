package slackhistory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestSlackClientListChannelHistoryPaginatesChronologically(t *testing.T) {
	t.Parallel()

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/conversations.history" {
			t.Fatalf("request path = %q, want %q", r.URL.Path, "/conversations.history")
		}
		if got := r.Header.Get("Authorization"); got != "Bearer xoxp-test" {
			t.Fatalf("Authorization header = %q, want bearer token", got)
		}
		requestCount++

		switch requestCount {
		case 1:
			assertSlackHistoryQuery(t, r.URL.Query(), map[string]string{
				"channel":   "C123",
				"oldest":    "1700.000002",
				"latest":    "1700.000007",
				"inclusive": "false",
				"limit":     "15",
			})
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"messages": []map[string]any{
					{"ts": "1700.000006", "type": "message", "text": "third", "user": "U333"},
					{"ts": "1700.000005", "type": "message", "text": "second", "bot_id": "B123", "reply_count": 2, "latest_reply": "1700.000005", "reply_users": []string{"U123", " U234 "}},
					{"ts": "1700.000002", "type": "message", "text": "too old"},
				},
				"response_metadata": map[string]any{
					"next_cursor": "cursor-2",
				},
			})
		case 2:
			assertSlackHistoryQuery(t, r.URL.Query(), map[string]string{
				"channel":   "C123",
				"oldest":    "1700.000002",
				"latest":    "1700.000007",
				"inclusive": "false",
				"limit":     "15",
				"cursor":    "cursor-2",
			})
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"messages": []map[string]any{
					{"ts": "1700.000004", "type": "message", "text": "first"},
					{"ts": "1700.000003", "type": "message", "subtype": "channel_join", "text": "joined"},
					{"ts": "1700.000007", "type": "message", "text": "too new"},
				},
			})
		default:
			t.Fatalf("received unexpected request #%d", requestCount)
		}
	}))
	defer server.Close()

	client := newTestSlackClient(t, server)
	result, err := client.ListChannelHistory(context.Background(), SlackListChannelHistoryParams{
		Channel:  "C123",
		AfterTS:  "1700.000002",
		BeforeTS: "1700.000007",
	})
	if err != nil {
		t.Fatalf("ListChannelHistory() error = %v", err)
	}
	if len(result.Messages) != 4 {
		t.Fatalf("len(ListChannelHistory().Messages) = %d, want 4", len(result.Messages))
	}
	if got := []string{result.Messages[0].TS, result.Messages[1].TS, result.Messages[2].TS, result.Messages[3].TS}; strings.Join(got, ",") != "1700.000003,1700.000004,1700.000005,1700.000006" {
		t.Fatalf("ListChannelHistory() message order = %v, want chronological timestamps", got)
	}
	if result.Messages[0].Subtype != "channel_join" {
		t.Fatalf("first message subtype = %q, want channel_join", result.Messages[0].Subtype)
	}
	if result.Messages[2].ReplyCount != 2 || result.Messages[2].LatestReply != "1700.000005" {
		t.Fatalf("reply metadata = %#v, want reply_count/latest_reply", result.Messages[2])
	}
	if got := strings.Join(result.Messages[2].ReplyUsers, ","); got != "U123,U234" {
		t.Fatalf("reply users = %q, want trimmed IDs", got)
	}
	if result.Truncated {
		t.Fatalf("result.Truncated = true, want false")
	}
	if result.NextCursor != "" {
		t.Fatalf("result.NextCursor = %q, want empty", result.NextCursor)
	}
}

func TestSlackClientListChannelHistoryRespectsMaxMessages(t *testing.T) {
	t.Parallel()

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		assertSlackHistoryQuery(t, r.URL.Query(), map[string]string{
			"channel":   "C123",
			"oldest":    "1700.000001",
			"inclusive": "false",
			"limit":     "2",
		})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"messages": []map[string]any{
				{"ts": "1700.000004", "type": "message", "text": "second"},
				{"ts": "1700.000003", "type": "message", "text": "first"},
			},
			"response_metadata": map[string]any{
				"next_cursor": "cursor-2",
			},
		})
	}))
	defer server.Close()

	client := newTestSlackClient(t, server)
	result, err := client.ListChannelHistory(context.Background(), SlackListChannelHistoryParams{
		Channel:     "C123",
		AfterTS:     "1700.000001",
		MaxMessages: 2,
	})
	if err != nil {
		t.Fatalf("ListChannelHistory() error = %v", err)
	}
	if requestCount != 1 {
		t.Fatalf("request count = %d, want 1", requestCount)
	}
	if !result.Truncated {
		t.Fatalf("result.Truncated = false, want true")
	}
	if result.NextCursor != "cursor-2" {
		t.Fatalf("result.NextCursor = %q, want %q", result.NextCursor, "cursor-2")
	}
	if got := []string{result.Messages[0].TS, result.Messages[1].TS}; strings.Join(got, ",") != "1700.000003,1700.000004" {
		t.Fatalf("ListChannelHistory() truncated message order = %v, want chronological timestamps", got)
	}
}

func TestSlackChannelHistoryToolUsesDefaultChannelAndReportsTruncation(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/conversations.history" {
			t.Fatalf("request path = %q, want %q", r.URL.Path, "/conversations.history")
		}
		assertSlackHistoryQuery(t, r.URL.Query(), map[string]string{
			"channel":   "C-default",
			"oldest":    "1700.000001",
			"latest":    "1700.000010",
			"inclusive": "false",
			"limit":     "1",
		})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"messages": []map[string]any{
				{"ts": "1700.000005", "type": "message", "text": "hello", "user": "U123"},
			},
			"response_metadata": map[string]any{
				"next_cursor": "cursor-2",
			},
		})
	}))
	defer server.Close()

	step := runSlackHistoryToolStep(t, map[string]any{
		"after_ts":     "1700.000001",
		"before_ts":    "1700.000010",
		"max_messages": 1,
	}, map[string]any{
		"default_channel": "C-default",
	}, true, map[string]string{
		SlackUserTokenEnvVar:  "xoxp-test",
		slackAPIBaseURLEnvVar: server.URL,
	})
	if step.Status != agent.StepStatusOK {
		t.Fatalf("step.Status = %q, want %q (error=%q)", step.Status, agent.StepStatusOK, step.Error)
	}
	var result SlackListChannelHistoryResult
	if err := json.Unmarshal([]byte(step.ActionOutput), &result); err != nil {
		t.Fatalf("Unmarshal(step.ActionOutput) error = %v", err)
	}
	if result.Channel != "C-default" {
		t.Fatalf("result.Channel = %q, want %q", result.Channel, "C-default")
	}
	if result.ReturnedCount != 1 {
		t.Fatalf("result.ReturnedCount = %d, want 1", result.ReturnedCount)
	}
	if !result.Truncated {
		t.Fatalf("result.Truncated = false, want true")
	}
	if result.NextCursor != "cursor-2" {
		t.Fatalf("result.NextCursor = %q, want %q", result.NextCursor, "cursor-2")
	}
}

func TestSlackChannelHistoryToolValidatesBounds(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	step := runSlackHistoryToolStep(t, map[string]any{
		"channel":   "C123",
		"after_ts":  "1700.000005",
		"before_ts": "1700.000004",
	}, nil, true, map[string]string{
		SlackUserTokenEnvVar: "xoxp-test",
	})
	if step.Status != agent.StepStatusPolicyError {
		t.Fatalf("step.Status = %q, want %q", step.Status, agent.StepStatusPolicyError)
	}
	if !strings.Contains(step.Error, "before_ts must be greater than after_ts") {
		t.Fatalf("step.Error = %q, want bounds validation error", step.Error)
	}
}

func runSlackHistoryToolStep(t *testing.T, input any, config map[string]any, networkEnabled bool, env map[string]string) agent.Step {
	t.Helper()

	inputJSON, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("json.Marshal(input) error = %v", err)
	}
	var decisionCount int
	var networkDialer interp.NetworkDialer
	if networkEnabled {
		networkDialer = interp.OSNetworkDialer{}
	}
	runtime, err := agent.New(agent.Config{
		Root:           t.TempDir(),
		NetworkEnabled: networkEnabled,
		NetworkDialer:  networkDialer,
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

func assertSlackHistoryQuery(t *testing.T, got url.Values, want map[string]string) {
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
