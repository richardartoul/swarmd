package datadogread

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/richardartoul/swarmd/pkg/agent"
	"github.com/richardartoul/swarmd/pkg/server"
	"github.com/richardartoul/swarmd/pkg/sh/interp"
)

var registerTestsOnce sync.Once

func registerTestTool(t *testing.T) {
	t.Helper()
	registerTestsOnce.Do(Register)
}

func TestDatadogReadToolRequiresNetworkCapability(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	_, err := agent.New(agent.Config{
		Root:           t.TempDir(),
		NetworkEnabled: false,
		ConfiguredTools: []agent.ConfiguredTool{{
			ID: ToolName,
		}},
		Driver: agent.DriverFunc(func(_ context.Context, _ agent.Request) (agent.Decision, error) {
			return agent.Decision{Finish: &agent.FinishAction{Value: "ok"}}, nil
		}),
	})
	if err == nil {
		t.Fatal("agent.New() error = nil, want Datadog network requirement")
	}
	if !strings.Contains(err.Error(), "requires network") {
		t.Fatalf("agent.New() error = %v, want requires network error", err)
	}
}

func TestNormalizeDatadogReadRequestDefaultsTimeWindow(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	req, err := NormalizeReadRequest(Input{
		Action: DatadogReadActionSearchLogs,
		Query:  "service:api",
	}, now)
	if err != nil {
		t.Fatalf("NormalizeReadRequest() error = %v", err)
	}
	if got := req.PageSize; got != DefaultPageSize {
		t.Fatalf("req.PageSize = %d, want %d", got, DefaultPageSize)
	}
	if got := req.To; !got.Equal(now) {
		t.Fatalf("req.To = %s, want %s", got, now)
	}
	if got := req.From; !got.Equal(now.Add(-DefaultLookback)) {
		t.Fatalf("req.From = %s, want %s", got, now.Add(-DefaultLookback))
	}
	if got := req.StorageTier; got != "" {
		t.Fatalf("req.StorageTier = %q, want empty", got)
	}
}

func TestNormalizeDatadogReadRequestAcceptsStorageTier(t *testing.T) {
	t.Parallel()

	req, err := NormalizeReadRequest(Input{
		Action:      DatadogReadActionSearchLogs,
		Query:       "service:api",
		StorageTier: " FLEX ",
	}, time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NormalizeReadRequest() error = %v", err)
	}
	if got := req.StorageTier; got != DatadogLogsStorageTierFlex {
		t.Fatalf("req.StorageTier = %q, want %q", got, DatadogLogsStorageTierFlex)
	}
}

func TestNormalizeDatadogReadRequestRejectsInvalidStorageTier(t *testing.T) {
	t.Parallel()

	_, err := NormalizeReadRequest(Input{
		Action:      DatadogReadActionSearchLogs,
		Query:       "service:api",
		StorageTier: "cold",
	}, time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("NormalizeReadRequest() error = nil, want invalid storage_tier error")
	}
	if !strings.Contains(err.Error(), "storage_tier") {
		t.Fatalf("NormalizeReadRequest() error = %v, want storage_tier error", err)
	}
}

func TestNormalizeDatadogReadRequestAggregateLogsDefaultsCountAndGroupLimit(t *testing.T) {
	t.Parallel()

	req, err := NormalizeReadRequest(Input{
		Action: DatadogReadActionAggregateLogs,
		Query:  "service:api",
		GroupBy: []logsAggregateGroupByInput{{
			Facet: "status",
		}},
	}, time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NormalizeReadRequest() error = %v", err)
	}
	if got := len(req.Compute); got != 1 {
		t.Fatalf("len(req.Compute) = %d, want 1", got)
	}
	if got := req.Compute[0].Aggregation; got != "count" {
		t.Fatalf("req.Compute[0].Aggregation = %q, want %q", got, "count")
	}
	if got := req.Compute[0].Type; got != "total" {
		t.Fatalf("req.Compute[0].Type = %q, want %q", got, "total")
	}
	if got := len(req.GroupBy); got != 1 {
		t.Fatalf("len(req.GroupBy) = %d, want 1", got)
	}
	if got := req.GroupBy[0].Limit; got != datadogDefaultAggregateGroupLimit {
		t.Fatalf("req.GroupBy[0].Limit = %d, want %d", got, datadogDefaultAggregateGroupLimit)
	}
}

func TestNormalizeDatadogReadRequestAggregateLogsAcceptsIndexesAndAlphabeticalCursor(t *testing.T) {
	t.Parallel()

	req, err := NormalizeReadRequest(Input{
		Action:     DatadogReadActionAggregateLogs,
		Query:      "service:api",
		Indexes:    []string{" main ", "main", ""},
		PageCursor: "cursor-1",
		GroupBy: []logsAggregateGroupByInput{{
			Facet: "service",
			Sort: &logsAggregateSortInput{
				Order: "asc",
			},
		}},
	}, time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NormalizeReadRequest() error = %v", err)
	}
	if got := len(req.Indexes); got != 1 {
		t.Fatalf("len(req.Indexes) = %d, want 1", got)
	}
	if got := req.Indexes[0]; got != "main" {
		t.Fatalf("req.Indexes[0] = %q, want %q", got, "main")
	}
	if req.GroupBy[0].Sort == nil {
		t.Fatal("req.GroupBy[0].Sort = nil, want alphabetical sort")
	}
	if got := req.GroupBy[0].Sort.Type; got != "alphabetical" {
		t.Fatalf("req.GroupBy[0].Sort.Type = %q, want %q", got, "alphabetical")
	}
	if got := req.GroupBy[0].Sort.Order; got != "asc" {
		t.Fatalf("req.GroupBy[0].Sort.Order = %q, want %q", got, "asc")
	}
}

func TestNormalizeDatadogReadRequestAggregateLogsRejectsMetriclessAvg(t *testing.T) {
	t.Parallel()

	_, err := NormalizeReadRequest(Input{
		Action: DatadogReadActionAggregateLogs,
		Query:  "service:api",
		Compute: []logsAggregateComputeInput{{
			Aggregation: "avg",
		}},
	}, time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("NormalizeReadRequest() error = nil, want metric validation error")
	}
	if !strings.Contains(err.Error(), "compute[0].metric") {
		t.Fatalf("NormalizeReadRequest() error = %v, want compute[0].metric error", err)
	}
}

func TestNormalizeDatadogReadRequestAggregateLogsRejectsPageCursorWithoutAlphabeticalSort(t *testing.T) {
	t.Parallel()

	_, err := NormalizeReadRequest(Input{
		Action:     DatadogReadActionAggregateLogs,
		Query:      "service:api",
		PageCursor: "cursor-1",
		GroupBy: []logsAggregateGroupByInput{{
			Facet: "status",
			Sort: &logsAggregateSortInput{
				Aggregation: "count",
				Order:       "desc",
			},
		}},
	}, time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("NormalizeReadRequest() error = nil, want alphabetical page_cursor validation error")
	}
	if !strings.Contains(err.Error(), "alphabetical") {
		t.Fatalf("NormalizeReadRequest() error = %v, want alphabetical page_cursor error", err)
	}
}

func TestDatadogReadToolRejectsUnexpectedArguments(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	step := runDatadogToolStep(t, map[string]any{
		"action": "get_incident",
		"query":  "should-not-be-here",
	}, true, nil, nil)
	if step.Status != agent.StepStatusPolicyError {
		t.Fatalf("step.Status = %q, want %q", step.Status, agent.StepStatusPolicyError)
	}
	if !strings.Contains(step.Error, "does not accept query") {
		t.Fatalf("step.Error = %q, want unexpected query error", step.Error)
	}
}

func TestDatadogReadToolUsesRuntimeHTTPClient(t *testing.T) {
	t.Parallel()
	registerTestTool(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/incidents" {
			t.Fatalf("request path = %q, want %q", r.URL.Path, "/api/v2/incidents")
		}
		if got := r.Header.Get("X-Test-Header"); got != "runtime-injected" {
			t.Fatalf("X-Test-Header = %q, want %q", got, "runtime-injected")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{
				"id":   "inc-123",
				"type": "incidents",
				"attributes": map[string]any{
					"title": "Runtime HTTP client works",
				},
			}},
		})
	}))
	defer server.Close()

	step := runDatadogToolStep(t, map[string]any{
		"action":    "list_incidents",
		"page_size": 1,
	}, true, map[string]string{
		DatadogAPIKeyEnvVar:  "api-key",
		DatadogAppKeyEnvVar:  "app-key",
		datadogBaseURLEnvVar: server.URL,
	}, []interp.HTTPHeaderRule{{
		Name:  "X-Test-Header",
		Value: "runtime-injected",
	}})
	if step.Status != agent.StepStatusOK {
		t.Fatalf("step.Status = %q, want %q (error=%q)", step.Status, agent.StepStatusOK, step.Error)
	}

	var output DatadogReadResult
	if err := json.Unmarshal([]byte(step.ActionOutput), &output); err != nil {
		t.Fatalf("json.Unmarshal(step.ActionOutput) error = %v", err)
	}
	items, ok := output.Items.([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("output.Items = %#v, want one incident", output.Items)
	}
}

func runDatadogToolStep(t *testing.T, input any, networkEnabled bool, env map[string]string, headers []interp.HTTPHeaderRule) agent.Step {
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
		HTTPHeaders:    append([]interp.HTTPHeaderRule(nil), headers...),
		ConfiguredTools: []agent.ConfiguredTool{{
			ID: ToolName,
		}},
		ToolRuntimeData: testRuntime{
			env: env,
		},
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
