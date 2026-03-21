package githubreadci

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestNormalizeReadRequestListWorkflowJobsDefaultsFilter(t *testing.T) {
	t.Parallel()

	req, err := normalizeReadRequest(input{
		Action: ActionListWorkflowJobs,
		Owner:  "acme",
		Repo:   "monorepo",
		RunID:  123,
	})
	if err != nil {
		t.Fatalf("normalizeReadRequest() error = %v", err)
	}
	if req.Filter != "latest" {
		t.Fatalf("req.Filter = %q, want %q", req.Filter, "latest")
	}
	if req.Pagination.PerPage != 100 {
		t.Fatalf("req.Pagination.PerPage = %d, want 100", req.Pagination.PerPage)
	}
}

func TestNormalizeReadRequestGetArtifactRejectsExtract(t *testing.T) {
	t.Parallel()

	_, err := normalizeReadRequest(input{
		Action:     ActionGetArtifact,
		Owner:      "acme",
		Repo:       "monorepo",
		ArtifactID: 701,
		Extract:    toolscommonBoolPtr(true),
	})
	if err == nil {
		t.Fatal("normalizeReadRequest() error = nil, want extract rejection for get_artifact")
	}
	if !strings.Contains(err.Error(), "extract") {
		t.Fatalf("normalizeReadRequest() error = %v, want extract field error", err)
	}
}

func TestCIToolListCheckRunsUsesRefPagination(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/monorepo/commits/abc123/check-runs" {
			t.Fatalf("request path = %q, want check-runs path", r.URL.Path)
		}
		if got := r.URL.Query().Get("page"); got != "1" {
			t.Fatalf("page = %q, want 1", got)
		}
		if got := r.URL.Query().Get("per_page"); got != "50" {
			t.Fatalf("per_page = %q, want 50", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"total_count": 1,
			"check_runs": []map[string]any{{
				"id":          501,
				"name":        "integration-tests",
				"status":      "completed",
				"conclusion":  "failure",
				"details_url": "https://github.com/acme/monorepo/actions/runs/123/jobs/987",
			}},
		})
	}))
	defer server.Close()

	step, _ := runCIToolStep(t, map[string]any{
		"action":   "list_check_runs",
		"owner":    "acme",
		"repo":     "monorepo",
		"ref":      "abc123",
		"page":     1,
		"per_page": 50,
	}, map[string]string{
		githubcommon.GitHubTokenEnvVar:      "github-token",
		githubcommon.GitHubAPIBaseURLEnvVar: server.URL,
	})
	if step.Status != agent.StepStatusOK {
		t.Fatalf("step.Status = %q, want %q (error=%q)", step.Status, agent.StepStatusOK, step.Error)
	}
	if !strings.Contains(step.ActionOutput, `"total_count":1`) {
		t.Fatalf("step.ActionOutput = %q, want check run payload", step.ActionOutput)
	}
}

func TestCIToolListWorkflowRunsAddsFilteredSearchWarning(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/monorepo/actions/runs" {
			t.Fatalf("request path = %q, want workflow runs path", r.URL.Path)
		}
		if got := r.URL.Query().Get("branch"); got != "main" {
			t.Fatalf("branch = %q, want main", got)
		}
		if got := r.URL.Query().Get("event"); got != "pull_request" {
			t.Fatalf("event = %q, want pull_request", got)
		}
		if got := r.URL.Query().Get("status"); got != "completed" {
			t.Fatalf("status = %q, want completed", got)
		}
		if got := r.URL.Query().Get("created"); got != ">=2026-03-15" {
			t.Fatalf("created = %q, want filter", got)
		}
		if got := r.URL.Query().Get("head_sha"); got != "abc123" {
			t.Fatalf("head_sha = %q, want abc123", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"workflow_runs": []map[string]any{{
				"id":          123,
				"name":        "ci",
				"workflow_id": 11,
				"head_branch": "main",
				"head_sha":    "abc123",
				"run_attempt": 2,
				"status":      "completed",
				"conclusion":  "success",
			}},
		})
	}))
	defer server.Close()

	step, _ := runCIToolStep(t, map[string]any{
		"action":   "list_workflow_runs",
		"owner":    "acme",
		"repo":     "monorepo",
		"branch":   "main",
		"event":    "pull_request",
		"status":   "completed",
		"created":  ">=2026-03-15",
		"head_sha": "abc123",
		"page":     1,
		"per_page": 20,
	}, map[string]string{
		githubcommon.GitHubTokenEnvVar:      "github-token",
		githubcommon.GitHubAPIBaseURLEnvVar: server.URL,
	})
	if step.Status != agent.StepStatusOK {
		t.Fatalf("step.Status = %q, want %q (error=%q)", step.Status, agent.StepStatusOK, step.Error)
	}
	if !strings.Contains(step.ActionOutput, "1000 workflow runs") {
		t.Fatalf("step.ActionOutput = %q, want filtered search warning", step.ActionOutput)
	}
}

func TestCIToolListWorkflowRunsForWorkflowUsesExtendedFilters(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/monorepo/actions/workflows/ci.yml/runs" {
			t.Fatalf("request path = %q, want workflow runs path", r.URL.Path)
		}
		if got := r.URL.Query().Get("branch"); got != "main" {
			t.Fatalf("branch = %q, want main", got)
		}
		if got := r.URL.Query().Get("event"); got != "pull_request" {
			t.Fatalf("event = %q, want pull_request", got)
		}
		if got := r.URL.Query().Get("status"); got != "completed" {
			t.Fatalf("status = %q, want completed", got)
		}
		if got := r.URL.Query().Get("created"); got != ">=2026-03-15" {
			t.Fatalf("created = %q, want filter", got)
		}
		if got := r.URL.Query().Get("head_sha"); got != "abc123" {
			t.Fatalf("head_sha = %q, want abc123", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"total_count": 1,
			"workflow_runs": []map[string]any{{
				"id":          123,
				"run_attempt": 2,
				"head_sha":    "abc123",
				"conclusion":  "success",
			}},
		})
	}))
	defer server.Close()

	step, _ := runCIToolStep(t, map[string]any{
		"action":      "list_workflow_runs_for_workflow",
		"owner":       "acme",
		"repo":        "monorepo",
		"workflow_id": "ci.yml",
		"branch":      "main",
		"event":       "pull_request",
		"status":      "completed",
		"created":     ">=2026-03-15",
		"head_sha":    "abc123",
	}, map[string]string{
		githubcommon.GitHubTokenEnvVar:      "github-token",
		githubcommon.GitHubAPIBaseURLEnvVar: server.URL,
	})
	if step.Status != agent.StepStatusOK {
		t.Fatalf("step.Status = %q, want %q (error=%q)", step.Status, agent.StepStatusOK, step.Error)
	}
	if !strings.Contains(step.ActionOutput, `"total_count":1`) {
		t.Fatalf("step.ActionOutput = %q, want total_count in workflow-scoped runs output", step.ActionOutput)
	}
}

func TestCIToolGetWorkflowJobIncludesStepStatus(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/monorepo/actions/jobs/987" {
			t.Fatalf("request path = %q, want workflow job path", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":          987,
			"name":        "integration-tests",
			"run_id":      123,
			"run_attempt": 2,
			"status":      "completed",
			"conclusion":  "failure",
			"steps": []map[string]any{{
				"number":     1,
				"name":       "Run tests",
				"status":     "completed",
				"conclusion": "failure",
			}},
		})
	}))
	defer server.Close()

	step, _ := runCIToolStep(t, map[string]any{
		"action": "get_workflow_job",
		"owner":  "acme",
		"repo":   "monorepo",
		"job_id": 987,
	}, map[string]string{
		githubcommon.GitHubTokenEnvVar:      "github-token",
		githubcommon.GitHubAPIBaseURLEnvVar: server.URL,
	})
	if step.Status != agent.StepStatusOK {
		t.Fatalf("step.Status = %q, want %q (error=%q)", step.Status, agent.StepStatusOK, step.Error)
	}
	if !strings.Contains(step.ActionOutput, `"status":"completed"`) {
		t.Fatalf("step.ActionOutput = %q, want step status in workflow job output", step.ActionOutput)
	}
}

func TestCIToolDownloadArtifactExtractsZip(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	zipBytes := mustZIP(t, map[string]string{"junit.xml": "<testsuite/>"})
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/monorepo/actions/artifacts/701":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":            701,
				"name":          "junit-results",
				"size_in_bytes": len(zipBytes),
				"expired":       false,
				"expires_at":    "2026-06-18T00:00:00Z",
				"workflow_run": map[string]any{
					"id":          123,
					"head_branch": "main",
					"head_sha":    "abc123",
				},
			})
		case "/repos/acme/monorepo/actions/artifacts/701/zip":
			http.Redirect(w, r, serverURL+"/downloads/701.zip", http.StatusFound)
		case "/downloads/701.zip":
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write(zipBytes)
		default:
			t.Fatalf("unexpected request path %q", r.URL.Path)
		}
	}))
	defer server.Close()
	serverURL = server.URL

	step, root := runCIToolStep(t, map[string]any{
		"action":      "download_artifact",
		"owner":       "acme",
		"repo":        "monorepo",
		"artifact_id": 701,
		"extract":     true,
	}, map[string]string{
		githubcommon.GitHubTokenEnvVar:      "github-token",
		githubcommon.GitHubAPIBaseURLEnvVar: server.URL,
	})
	if step.Status != agent.StepStatusOK {
		t.Fatalf("step.Status = %q, want %q (error=%q)", step.Status, agent.StepStatusOK, step.Error)
	}

	extractedPath := filepath.Join(root, "github/actions/artifacts/701/junit.xml")
	bytes, err := os.ReadFile(extractedPath)
	if err != nil {
		t.Fatalf("os.ReadFile(extractedPath) error = %v", err)
	}
	if string(bytes) != "<testsuite/>" {
		t.Fatalf("extracted file = %q, want junit payload", string(bytes))
	}
	if !strings.Contains(step.ActionOutput, `"redirected":true`) {
		t.Fatalf("step.ActionOutput = %q, want redirected metadata", step.ActionOutput)
	}
}

func runCIToolStep(t *testing.T, input any, env map[string]string) (agent.Step, string) {
	t.Helper()

	root := t.TempDir()
	inputJSON, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("json.Marshal(input) error = %v", err)
	}
	var decisionCount int
	runtime, err := agent.New(agent.Config{
		Root:           root,
		NetworkEnabled: true,
		NetworkDialer:  interp.OSNetworkDialer{},
		ConfiguredTools: []agent.ConfiguredTool{{
			ID: ToolName,
		}},
		ToolRuntimeData: ciTestRuntime{env: env},
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
	return result.Steps[0], root
}

type ciTestRuntime struct {
	env map[string]string
}

func (r ciTestRuntime) NamespaceID() string { return "default" }
func (r ciTestRuntime) AgentID() string     { return "worker" }
func (r ciTestRuntime) LookupEnv(name string) string {
	return r.env[name]
}
func (r ciTestRuntime) Logger() server.ToolLogger { return nil }

func mustZIP(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, contents := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("writer.Create(%q) error = %v", name, err)
		}
		if _, err := entry.Write([]byte(contents)); err != nil {
			t.Fatalf("entry.Write(%q) error = %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close() error = %v", err)
	}
	return buffer.Bytes()
}

func toolscommonBoolPtr(value bool) *bool {
	return &value
}
