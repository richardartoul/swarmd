package githubreadrepo

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

func TestNormalizeReadRequestSearchCodeRequiresQuery(t *testing.T) {
	t.Parallel()

	_, err := normalizeReadRequest(input{
		Action: ActionSearchCode,
		Owner:  "acme",
		Repo:   "monorepo",
	})
	if err == nil {
		t.Fatal("normalizeReadRequest() error = nil, want missing query error")
	}
	if !strings.Contains(err.Error(), "query") {
		t.Fatalf("normalizeReadRequest() error = %v, want query error", err)
	}
}

func TestRepoToolSearchCodeScopesRepositoryAndReturnsWarning(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/code" {
			t.Fatalf("request path = %q, want /search/code", r.URL.Path)
		}
		query := r.URL.Query().Get("q")
		if !strings.Contains(query, "repo:acme/monorepo") {
			t.Fatalf("query q = %q, want repo qualifier", query)
		}
		if got := r.URL.Query().Get("page"); got != "1" {
			t.Fatalf("page = %q, want 1", got)
		}
		if got := r.URL.Query().Get("per_page"); got != "25" {
			t.Fatalf("per_page = %q, want 25", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"total_count":        1,
			"incomplete_results": false,
			"items": []map[string]any{{
				"path":     "services/payments/flaky_test.go",
				"sha":      "abc123",
				"html_url": "https://github.com/acme/monorepo/blob/main/services/payments/flaky_test.go",
				"repository": map[string]any{
					"full_name": "acme/monorepo",
				},
			}},
		})
	}))
	defer server.Close()

	step := runRepoToolStep(t, map[string]any{
		"action":   "search_code",
		"owner":    "acme",
		"repo":     "monorepo",
		"query":    "FlakyTest path:services/payments language:go",
		"page":     1,
		"per_page": 25,
	}, map[string]string{
		githubcommon.GitHubTokenEnvVar:      "github-token",
		githubcommon.GitHubAPIBaseURLEnvVar: server.URL,
	})
	if step.Status != agent.StepStatusOK {
		t.Fatalf("step.Status = %q, want %q (error=%q)", step.Status, agent.StepStatusOK, step.Error)
	}

	var output githubcommon.ToolResponse
	if err := json.Unmarshal([]byte(step.ActionOutput), &output); err != nil {
		t.Fatalf("json.Unmarshal(step.ActionOutput) error = %v", err)
	}
	if !output.OK {
		t.Fatalf("output.OK = false, want true: %#v", output)
	}
	if len(output.Warnings) == 0 || !strings.Contains(output.Warnings[0], "default branch") {
		t.Fatalf("output.Warnings = %#v, want default branch warning", output.Warnings)
	}
}

func TestRepoToolGetFileContentsDecodesBase64(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/monorepo/contents/services/payments/flaky_test.go" {
			t.Fatalf("request path = %q, want contents path", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"path":     "services/payments/flaky_test.go",
			"sha":      "abc123",
			"size":     5,
			"encoding": "base64",
			"content":  "aGVsbG8=",
		})
	}))
	defer server.Close()

	step := runRepoToolStep(t, map[string]any{
		"action": "get_file_contents",
		"owner":  "acme",
		"repo":   "monorepo",
		"path":   "services/payments/flaky_test.go",
	}, map[string]string{
		githubcommon.GitHubTokenEnvVar:      "github-token",
		githubcommon.GitHubAPIBaseURLEnvVar: server.URL,
	})
	if step.Status != agent.StepStatusOK {
		t.Fatalf("step.Status = %q, want %q (error=%q)", step.Status, agent.StepStatusOK, step.Error)
	}
	if !strings.Contains(step.ActionOutput, `"content_inline":"hello"`) {
		t.Fatalf("step.ActionOutput = %q, want decoded content_inline", step.ActionOutput)
	}
}

func TestRepoToolGetFileContentsNormalizesLeadingSlash(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/monorepo/contents/services/payments/flaky_test.go" {
			t.Fatalf("request path = %q, want normalized contents path", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"path":     "services/payments/flaky_test.go",
			"sha":      "abc123",
			"size":     5,
			"encoding": "base64",
			"content":  "aGVsbG8=",
		})
	}))
	defer server.Close()

	step := runRepoToolStep(t, map[string]any{
		"action": "get_file_contents",
		"owner":  "acme",
		"repo":   "monorepo",
		"path":   "/services/payments/flaky_test.go",
	}, map[string]string{
		githubcommon.GitHubTokenEnvVar:      "github-token",
		githubcommon.GitHubAPIBaseURLEnvVar: server.URL,
	})
	if step.Status != agent.StepStatusOK {
		t.Fatalf("step.Status = %q, want %q (error=%q)", step.Status, agent.StepStatusOK, step.Error)
	}
}

func TestRepoToolGetFileContentsWarnsWhenInlineContentIsUnavailable(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/monorepo/contents/large.bin" {
			t.Fatalf("request path = %q, want contents path", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type":         "file",
			"path":         "large.bin",
			"sha":          "abc123",
			"size":         2048,
			"encoding":     "none",
			"content":      nil,
			"download_url": "https://raw.githubusercontent.com/acme/monorepo/main/large.bin",
		})
	}))
	defer server.Close()

	step := runRepoToolStep(t, map[string]any{
		"action": "get_file_contents",
		"owner":  "acme",
		"repo":   "monorepo",
		"path":   "large.bin",
	}, map[string]string{
		githubcommon.GitHubTokenEnvVar:      "github-token",
		githubcommon.GitHubAPIBaseURLEnvVar: server.URL,
	})
	if step.Status != agent.StepStatusOK {
		t.Fatalf("step.Status = %q, want %q (error=%q)", step.Status, agent.StepStatusOK, step.Error)
	}
	if !strings.Contains(step.ActionOutput, `"download_url":"https://raw.githubusercontent.com/acme/monorepo/main/large.bin"`) {
		t.Fatalf("step.ActionOutput = %q, want download_url", step.ActionOutput)
	}
	if !strings.Contains(step.ActionOutput, `GitHub did not include inline file content`) {
		t.Fatalf("step.ActionOutput = %q, want inline content warning", step.ActionOutput)
	}
}

func TestRepoToolGetFileContentsHandlesSubmoduleMetadata(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/monorepo/contents/vendor/lib" {
			t.Fatalf("request path = %q, want contents path", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type":              "submodule",
			"path":              "vendor/lib",
			"sha":               "abc123",
			"submodule_git_url": "https://github.com/acme/lib.git",
		})
	}))
	defer server.Close()

	step := runRepoToolStep(t, map[string]any{
		"action": "get_file_contents",
		"owner":  "acme",
		"repo":   "monorepo",
		"path":   "vendor/lib",
	}, map[string]string{
		githubcommon.GitHubTokenEnvVar:      "github-token",
		githubcommon.GitHubAPIBaseURLEnvVar: server.URL,
	})
	if step.Status != agent.StepStatusOK {
		t.Fatalf("step.Status = %q, want %q (error=%q)", step.Status, agent.StepStatusOK, step.Error)
	}
	if !strings.Contains(step.ActionOutput, `"type":"submodule"`) || !strings.Contains(step.ActionOutput, `"submodule_git_url":"https://github.com/acme/lib.git"`) {
		t.Fatalf("step.ActionOutput = %q, want submodule metadata", step.ActionOutput)
	}
	if !strings.Contains(step.ActionOutput, `submodule entry without inline file content`) {
		t.Fatalf("step.ActionOutput = %q, want submodule warning", step.ActionOutput)
	}
}

func TestRepoToolListTreeIncludesZeroSizeBlob(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/monorepo/git/trees/main" {
			t.Fatalf("request path = %q, want git tree path", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sha":       "tree123",
			"truncated": false,
			"tree": []map[string]any{{
				"path": "empty.txt",
				"type": "blob",
				"size": 0,
				"sha":  "blob123",
			}},
		})
	}))
	defer server.Close()

	step := runRepoToolStep(t, map[string]any{
		"action": "list_tree",
		"owner":  "acme",
		"repo":   "monorepo",
		"ref":    "main",
	}, map[string]string{
		githubcommon.GitHubTokenEnvVar:      "github-token",
		githubcommon.GitHubAPIBaseURLEnvVar: server.URL,
	})
	if step.Status != agent.StepStatusOK {
		t.Fatalf("step.Status = %q, want %q (error=%q)", step.Status, agent.StepStatusOK, step.Error)
	}
	if !strings.Contains(step.ActionOutput, `"size":0`) {
		t.Fatalf("step.ActionOutput = %q, want zero-sized blob entry", step.ActionOutput)
	}
}

func TestRepoToolListRulesetsUsesPagination(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/monorepo/rulesets" {
			t.Fatalf("request path = %q, want rulesets path", r.URL.Path)
		}
		if got := r.URL.Query().Get("page"); got != "1" {
			t.Fatalf("page = %q, want 1", got)
		}
		if got := r.URL.Query().Get("per_page"); got != "30" {
			t.Fatalf("per_page = %q, want 30", got)
		}
		w.Header().Set("Link", `<https://api.github.com/repos/acme/monorepo/rulesets?page=2&per_page=30>; rel="next"`)
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id":          99,
			"name":        "Protect main",
			"target":      "branch",
			"enforcement": "active",
		}})
	}))
	defer server.Close()

	step := runRepoToolStep(t, map[string]any{
		"action": "list_rulesets",
		"owner":  "acme",
		"repo":   "monorepo",
	}, map[string]string{
		githubcommon.GitHubTokenEnvVar:      "github-token",
		githubcommon.GitHubAPIBaseURLEnvVar: server.URL,
	})
	if step.Status != agent.StepStatusOK {
		t.Fatalf("step.Status = %q, want %q (error=%q)", step.Status, agent.StepStatusOK, step.Error)
	}
	if !strings.Contains(step.ActionOutput, `"has_next_page":true`) || !strings.Contains(step.ActionOutput, `"next_page":2`) {
		t.Fatalf("step.ActionOutput = %q, want ruleset pagination metadata", step.ActionOutput)
	}
}

func runRepoToolStep(t *testing.T, input any, env map[string]string) agent.Step {
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
		ToolRuntimeData: repoTestRuntime{env: env},
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

type repoTestRuntime struct {
	env map[string]string
}

func (r repoTestRuntime) NamespaceID() string { return "default" }
func (r repoTestRuntime) AgentID() string     { return "worker" }
func (r repoTestRuntime) LookupEnv(name string) string {
	return r.env[name]
}
func (r repoTestRuntime) Logger() server.ToolLogger { return nil }
