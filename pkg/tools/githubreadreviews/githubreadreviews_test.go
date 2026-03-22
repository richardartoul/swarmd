package githubreadreviews

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
	"github.com/richardartoul/swarmd/pkg/tools/githubcommon"
)

var registerTestsOnce sync.Once

func registerTestTool(t *testing.T) {
	t.Helper()
	registerTestsOnce.Do(Register)
}

func TestNormalizeReadRequestSearchIssuesDefaultsKind(t *testing.T) {
	t.Parallel()

	req, err := normalizeReadRequest(input{
		Action: ActionSearchIssues,
		Owner:  "acme",
		Repo:   "monorepo",
		Query:  "label:flaky-test state:open",
	})
	if err != nil {
		t.Fatalf("normalizeReadRequest() error = %v", err)
	}
	if req.Kind != "issues" {
		t.Fatalf("req.Kind = %q, want %q", req.Kind, "issues")
	}
	if req.Pagination.Page != 1 || req.Pagination.PerPage != 20 {
		t.Fatalf("req.Pagination = %#v, want page=1 per_page=20", req.Pagination)
	}
}

func TestReviewsToolSearchIssuesScopesQuery(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/issues" {
			t.Fatalf("request path = %q, want /search/issues", r.URL.Path)
		}
		query := r.URL.Query().Get("q")
		if !strings.Contains(query, "repo:acme/monorepo") {
			t.Fatalf("query q = %q, want repo qualifier", query)
		}
		if !strings.Contains(query, "is:issue") {
			t.Fatalf("query q = %q, want issue qualifier", query)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"total_count":        1,
			"incomplete_results": false,
			"items": []map[string]any{{
				"number": 4182,
				"title":  "Flaky integration test in payments",
				"state":  "open",
				"labels": []map[string]any{{"name": "flaky-test"}},
			}},
		})
	}))
	defer server.Close()

	step := runReviewsToolStep(t, map[string]any{
		"action":   "search_issues",
		"owner":    "acme",
		"repo":     "monorepo",
		"query":    "label:flaky-test state:open",
		"page":     1,
		"per_page": 20,
	}, map[string]string{
		githubcommon.GitHubTokenEnvVar:      "github-token",
		githubcommon.GitHubAPIBaseURLEnvVar: server.URL,
	})
	if step.Status != agent.StepStatusOK {
		t.Fatalf("step.Status = %q, want %q (error=%q)", step.Status, agent.StepStatusOK, step.Error)
	}
	if !strings.Contains(step.ActionOutput, `"kind":"issue"`) {
		t.Fatalf("step.ActionOutput = %q, want normalized issue kind", step.ActionOutput)
	}
}

func TestReviewsToolCompareRefsReturnsCompareSummary(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/monorepo/compare/main...fix%2Fflaky-payment-retry" {
			t.Fatalf("request path = %q, want compare path", r.URL.Path)
		}
		if got := r.URL.Query().Get("page"); got != "1" {
			t.Fatalf("page = %q, want 1", got)
		}
		if got := r.URL.Query().Get("per_page"); got != "100" {
			t.Fatalf("per_page = %q, want 100", got)
		}
		w.Header().Set("Link", `<https://api.github.com/repos/acme/monorepo/compare/main...fix%2Fflaky-payment-retry?page=2&per_page=100>; rel="next"`)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":    "ahead",
			"ahead_by":  2,
			"behind_by": 0,
			"files": []map[string]any{{
				"filename": "services/payments/flaky_test.go",
				"status":   "modified",
				"changes":  26,
			}},
		})
	}))
	defer server.Close()

	step := runReviewsToolStep(t, map[string]any{
		"action": "compare_refs",
		"owner":  "acme",
		"repo":   "monorepo",
		"base":   "main",
		"head":   "fix/flaky-payment-retry",
	}, map[string]string{
		githubcommon.GitHubTokenEnvVar:      "github-token",
		githubcommon.GitHubAPIBaseURLEnvVar: server.URL,
	})
	if step.Status != agent.StepStatusOK {
		t.Fatalf("step.Status = %q, want %q (error=%q)", step.Status, agent.StepStatusOK, step.Error)
	}
	if !strings.Contains(step.ActionOutput, `"ahead_by":2`) {
		t.Fatalf("step.ActionOutput = %q, want compare summary", step.ActionOutput)
	}
	if !strings.Contains(step.ActionOutput, `"has_next_page":true`) {
		t.Fatalf("step.ActionOutput = %q, want compare pagination metadata", step.ActionOutput)
	}
}

func TestReviewsToolIssueTimelineUsesUserFallback(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/monorepo/issues/42/timeline" {
			t.Fatalf("request path = %q, want issue timeline path", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"event":      "commented",
			"created_at": "2026-03-20T00:00:00Z",
			"user": map[string]any{
				"login": "alice",
			},
		}})
	}))
	defer server.Close()

	step := runReviewsToolStep(t, map[string]any{
		"action":       "get_issue_timeline",
		"owner":        "acme",
		"repo":         "monorepo",
		"issue_number": 42,
	}, map[string]string{
		githubcommon.GitHubTokenEnvVar:      "github-token",
		githubcommon.GitHubAPIBaseURLEnvVar: server.URL,
	})
	if step.Status != agent.StepStatusOK {
		t.Fatalf("step.Status = %q, want %q (error=%q)", step.Status, agent.StepStatusOK, step.Error)
	}
	if !strings.Contains(step.ActionOutput, `"actor":"alice"`) {
		t.Fatalf("step.ActionOutput = %q, want user fallback actor", step.ActionOutput)
	}
}

func TestReviewsToolListCommitsOmitsSyntheticDefaultRef(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/monorepo/commits" {
			t.Fatalf("request path = %q, want commits path", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"sha": "abc123",
			"commit": map[string]any{
				"message": "Fix flaky test",
				"author": map[string]any{
					"name": "Alice",
					"date": "2026-03-20T00:00:00Z",
				},
			},
		}})
	}))
	defer server.Close()

	step := runReviewsToolStep(t, map[string]any{
		"action": "list_commits",
		"owner":  "acme",
		"repo":   "monorepo",
	}, map[string]string{
		githubcommon.GitHubTokenEnvVar:      "github-token",
		githubcommon.GitHubAPIBaseURLEnvVar: server.URL,
	})
	if step.Status != agent.StepStatusOK {
		t.Fatalf("step.Status = %q, want %q (error=%q)", step.Status, agent.StepStatusOK, step.Error)
	}
	if strings.Contains(step.ActionOutput, `"ref":"default"`) {
		t.Fatalf("step.ActionOutput = %q, want synthetic default ref omitted", step.ActionOutput)
	}
}

func runReviewsToolStep(t *testing.T, input any, env map[string]string) agent.Step {
	t.Helper()

	inputJSON, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("json.Marshal(input) error = %v", err)
	}
	var decisionCount int
	runtime, err := agent.New(agent.Config{
		Root:                 t.TempDir(),
		NetworkDialer:        interp.OSNetworkDialer{},
		GlobalReachableHosts: []interp.HostMatcher{{Glob: "*"}},
		ConfiguredTools: []agent.ConfiguredTool{{
			ID: ToolName,
		}},
		ToolRuntimeData: reviewsTestRuntime{env: env},
		Driver: agent.DriverFunc(func(_ context.Context, _ agent.Request) (agent.Decision, error) {
			if decisionCount == 0 {
				decisionCount++
				return agent.Decision{
					Tool: &agent.ToolAction{
						Name:  ToolName,
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

	result, err := runtime.HandleTrigger(context.Background(), agent.Trigger{ID: "trigger-test", Kind: "test"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("len(result.Steps) = %d, want 1", len(result.Steps))
	}
	return result.Steps[0]
}

type reviewsTestRuntime struct {
	env map[string]string
}

func (r reviewsTestRuntime) NamespaceID() string { return "default" }
func (r reviewsTestRuntime) AgentID() string     { return "worker" }
func (r reviewsTestRuntime) LookupEnv(name string) string {
	return r.env[name]
}
func (r reviewsTestRuntime) Logger() server.ToolLogger { return nil }
