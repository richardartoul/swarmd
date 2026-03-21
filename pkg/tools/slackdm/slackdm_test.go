package slackdm

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

func TestSlackDMToolUsesUserID(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/conversations.open":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			if got := body["users"]; got != "U123" {
				t.Fatalf("request users = %#v, want %q", got, "U123")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"channel": map[string]any{
					"id": "D123",
				},
			})
		case "/chat.postMessage":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			if got := body["channel"]; got != "D123" {
				t.Fatalf("request channel = %#v, want %q", got, "D123")
			}
			if got := body["text"]; got != "hello via dm" {
				t.Fatalf("request text = %#v, want %q", got, "hello via dm")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":      true,
				"channel": "D123",
				"ts":      "1700.000001",
				"message": map[string]any{
					"ts": "1700.000001",
				},
			})
		default:
			t.Fatalf("unexpected request path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	step := runSlackDMToolStep(t, map[string]any{
		"user_id": "U123",
		"text":    "hello via dm",
	}, true, map[string]string{
		SlackUserTokenEnvVar:  "xoxp-test",
		slackAPIBaseURLEnvVar: server.URL,
	})
	if step.Status != agent.StepStatusOK {
		t.Fatalf("step.Status = %q, want %q (error=%q)", step.Status, agent.StepStatusOK, step.Error)
	}
	if got := strings.Join(paths, ","); got != "/conversations.open,/chat.postMessage" {
		t.Fatalf("request paths = %q, want open then post", got)
	}

	var result SlackDirectMessageResult
	if err := json.Unmarshal([]byte(step.ActionOutput), &result); err != nil {
		t.Fatalf("Unmarshal(step.ActionOutput) error = %v", err)
	}
	if result.UserID != "U123" || result.Channel != "D123" || result.TS != "1700.000001" || result.ThreadTS != "1700.000001" {
		t.Fatalf("tool result = %#v, want resolved user and dm post metadata", result)
	}
	if result.Email != "" {
		t.Fatalf("tool result email = %q, want empty for direct user_id path", result.Email)
	}
}

func TestSlackDMToolUsesEmailAndCachesLookups(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	email := uniqueTestEmail(t)
	var (
		lookupRequests int
		openRequests   int
		postRequests   int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users.lookupByEmail":
			lookupRequests++
			if got := r.URL.Query().Get("email"); got != email {
				t.Fatalf("query email = %q, want %q", got, email)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"user": map[string]any{
					"id": "U234",
					"profile": map[string]any{
						"email": email,
					},
				},
			})
		case "/conversations.open":
			openRequests++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"channel": map[string]any{
					"id": "D234",
				},
			})
		case "/chat.postMessage":
			postRequests++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":      true,
				"channel": "D234",
				"ts":      "1700.00000" + string(rune('0'+postRequests)),
				"message": map[string]any{
					"ts": "1700.00000" + string(rune('0'+postRequests)),
				},
			})
		default:
			t.Fatalf("unexpected request path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	env := map[string]string{
		SlackUserTokenEnvVar:  "xoxp-test",
		slackAPIBaseURLEnvVar: server.URL,
	}
	firstStep := runSlackDMToolStep(t, map[string]any{
		"email": email,
		"text":  "first hello",
	}, true, env)
	secondStep := runSlackDMToolStep(t, map[string]any{
		"email": strings.ToUpper(email),
		"text":  "second hello",
	}, true, env)
	if firstStep.Status != agent.StepStatusOK || secondStep.Status != agent.StepStatusOK {
		t.Fatalf("steps = %#v %#v, want both OK", firstStep.Status, secondStep.Status)
	}
	if lookupRequests != 1 {
		t.Fatalf("lookup request count = %d, want 1 cache-backed lookup", lookupRequests)
	}
	if openRequests != 2 || postRequests != 2 {
		t.Fatalf("open/post counts = %d/%d, want 2/2", openRequests, postRequests)
	}

	var result SlackDirectMessageResult
	if err := json.Unmarshal([]byte(secondStep.ActionOutput), &result); err != nil {
		t.Fatalf("Unmarshal(secondStep.ActionOutput) error = %v", err)
	}
	if result.UserID != "U234" || result.Email != email || result.Channel != "D234" {
		t.Fatalf("second tool result = %#v, want cached email resolution and dm channel", result)
	}
}

func TestSlackDMToolRejectsEmptyText(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	step := runSlackDMToolStep(t, map[string]any{
		"user_id": "U123",
	}, true, map[string]string{
		SlackUserTokenEnvVar: "xoxp-test",
	})
	if step.Status != agent.StepStatusPolicyError {
		t.Fatalf("step.Status = %q, want %q", step.Status, agent.StepStatusPolicyError)
	}
	if !strings.Contains(step.Error, "text must not be empty") {
		t.Fatalf("step.Error = %q, want missing text error", step.Error)
	}
}

func TestSlackDMToolRejectsMissingRecipient(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	step := runSlackDMToolStep(t, map[string]any{
		"text": "hello",
	}, true, map[string]string{
		SlackUserTokenEnvVar: "xoxp-test",
	})
	if step.Status != agent.StepStatusPolicyError {
		t.Fatalf("step.Status = %q, want %q", step.Status, agent.StepStatusPolicyError)
	}
	if !strings.Contains(step.Error, "exactly one of user_id or email must be provided") {
		t.Fatalf("step.Error = %q, want recipient validation error", step.Error)
	}
}

func TestSlackDMToolRejectsDuplicateRecipients(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	step := runSlackDMToolStep(t, map[string]any{
		"user_id": "U123",
		"email":   uniqueTestEmail(t),
		"text":    "hello",
	}, true, map[string]string{
		SlackUserTokenEnvVar: "xoxp-test",
	})
	if step.Status != agent.StepStatusPolicyError {
		t.Fatalf("step.Status = %q, want %q", step.Status, agent.StepStatusPolicyError)
	}
	if !strings.Contains(step.Error, "exactly one of user_id or email must be provided") {
		t.Fatalf("step.Error = %q, want duplicate recipient validation error", step.Error)
	}
}

func TestSlackDMToolReportsSlackAPIErrorsAsPolicyErrors(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	email := uniqueTestEmail(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users.lookupByEmail" {
			t.Fatalf("unexpected request path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":       false,
			"error":    "missing_scope",
			"needed":   "users:read.email",
			"provided": "chat:write",
		})
	}))
	defer server.Close()

	step := runSlackDMToolStep(t, map[string]any{
		"email": email,
		"text":  "hello",
	}, true, map[string]string{
		SlackUserTokenEnvVar:  "xoxp-test",
		slackAPIBaseURLEnvVar: server.URL,
	})
	if step.Status != agent.StepStatusPolicyError {
		t.Fatalf("step.Status = %q, want %q", step.Status, agent.StepStatusPolicyError)
	}
	if !strings.Contains(step.Error, "missing_scope") || !strings.Contains(step.Error, "users:read.email") {
		t.Fatalf("step.Error = %q, want formatted Slack API error", step.Error)
	}
}

func TestSlackDMToolRequiresNetworkCapability(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	_, err := agent.New(agent.Config{
		Root:           t.TempDir(),
		NetworkEnabled: false,
		ConfiguredTools: []agent.ConfiguredTool{{
			ID: toolName,
		}},
		Driver: agent.DriverFunc(func(_ context.Context, _ agent.Request) (agent.Decision, error) {
			return agent.Decision{Finish: &agent.FinishAction{Value: "ok"}}, nil
		}),
	})
	if err == nil {
		t.Fatal("agent.New() error = nil, want slack network requirement")
	}
	if !strings.Contains(err.Error(), "requires network") {
		t.Fatalf("agent.New() error = %v, want requires network error", err)
	}
}

func runSlackDMToolStep(t *testing.T, input any, networkEnabled bool, env map[string]string) agent.Step {
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
			ID: toolName,
		}},
		ToolRuntimeData: dmTestRuntime{env: env},
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

func uniqueTestEmail(t *testing.T) string {
	t.Helper()
	name := strings.ToLower(t.Name())
	name = strings.ReplaceAll(name, "/", "-")
	name = strings.ReplaceAll(name, "_", "-")
	return "slackdm-" + name + "@example.com"
}

type dmTestRuntime struct {
	env map[string]string
}

func (r dmTestRuntime) NamespaceID() string {
	return "default"
}

func (r dmTestRuntime) AgentID() string {
	return "worker"
}

func (r dmTestRuntime) LookupEnv(name string) string {
	if r.env == nil {
		return ""
	}
	return r.env[name]
}

func (r dmTestRuntime) Logger() server.ToolLogger {
	return nil
}
