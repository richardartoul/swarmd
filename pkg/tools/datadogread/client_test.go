package datadogread

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	datadogapi "github.com/DataDog/datadog-api-client-go/v2/api/datadog"
)

func TestNewDatadogClientConfiguresRetrySiteAndUnstableOps(t *testing.T) {
	t.Parallel()

	client, err := NewDatadogClient(DatadogClientConfig{
		APIKey: "api-key",
		AppKey: "app-key",
		Site:   "datadoghq.eu",
	})
	if err != nil {
		t.Fatalf("NewDatadogClient() error = %v", err)
	}
	if !client.apiClient.Cfg.RetryConfiguration.EnableRetry {
		t.Fatal("RetryConfiguration.EnableRetry = false, want true")
	}
	if got := client.apiClient.Cfg.RetryConfiguration.MaxRetries; got != datadogDefaultMaxRetries {
		t.Fatalf("RetryConfiguration.MaxRetries = %d, want %d", got, datadogDefaultMaxRetries)
	}
	for _, operation := range datadogUnstableOperations {
		if !client.apiClient.Cfg.IsUnstableOperationEnabled(operation) {
			t.Fatalf("operation %q was not enabled", operation)
		}
	}

	ctx := client.requestContext(context.Background())
	serverVars, ok := ctx.Value(datadogapi.ContextServerVariables).(map[string]string)
	if !ok {
		t.Fatal("request context did not include server variables")
	}
	if got := serverVars["site"]; got != "datadoghq.eu" {
		t.Fatalf("site = %q, want %q", got, "datadoghq.eu")
	}
	apiKeys, ok := ctx.Value(datadogapi.ContextAPIKeys).(map[string]datadogapi.APIKey)
	if !ok {
		t.Fatal("request context did not include API keys")
	}
	if got := apiKeys["apiKeyAuth"].Key; got != "api-key" {
		t.Fatalf("apiKeyAuth = %q, want %q", got, "api-key")
	}
	if got := apiKeys["appKeyAuth"].Key; got != "app-key" {
		t.Fatalf("appKeyAuth = %q, want %q", got, "app-key")
	}
}

func TestDatadogClientListIncidentsNormalizesResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/incidents" {
			t.Fatalf("request path = %q, want %q", r.URL.Path, "/api/v2/incidents")
		}
		if got := r.Header.Get("DD-API-KEY"); got != "api-key" {
			t.Fatalf("DD-API-KEY = %q, want %q", got, "api-key")
		}
		if got := r.Header.Get("DD-APPLICATION-KEY"); got != "app-key" {
			t.Fatalf("DD-APPLICATION-KEY = %q, want %q", got, "app-key")
		}
		if got := r.URL.Query().Get("page[size]"); got != "10" {
			t.Fatalf("page[size] = %q, want %q", got, "10")
		}
		if got := r.URL.Query().Get("page[offset]"); got != "20" {
			t.Fatalf("page[offset] = %q, want %q", got, "20")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{
				"id":   "inc-123",
				"type": "incidents",
				"attributes": map[string]any{
					"title":                 "API outage",
					"public_id":             42,
					"severity":              "SEV-2",
					"state":                 "active",
					"created":               "2026-03-15T00:00:00Z",
					"modified":              "2026-03-15T00:05:00Z",
					"customer_impacted":     true,
					"customer_impact_scope": "login failures",
				},
			}},
			"meta": map[string]any{
				"pagination": map[string]any{
					"offset":      20,
					"size":        10,
					"next_offset": 30,
				},
			},
		})
	}))
	defer server.Close()

	client := newTestDatadogClient(t, server)
	result, err := client.ExecuteRead(context.Background(), DatadogReadRequest{
		Action:     DatadogReadActionListIncidents,
		PageSize:   10,
		PageOffset: 20,
	})
	if err != nil {
		t.Fatalf("ExecuteRead(list incidents) error = %v", err)
	}
	items, ok := result.Items.([]DatadogIncident)
	if !ok {
		t.Fatalf("result.Items type = %T, want []DatadogIncident", result.Items)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if got := items[0].Title; got != "API outage" {
		t.Fatalf("items[0].Title = %q, want %q", got, "API outage")
	}
	if items[0].CustomerImpacted == nil || !*items[0].CustomerImpacted {
		t.Fatalf("items[0].CustomerImpacted = %#v, want true", items[0].CustomerImpacted)
	}
	if result.NextOffset == nil || *result.NextOffset != 30 {
		t.Fatalf("result.NextOffset = %#v, want 30", result.NextOffset)
	}
}

func TestDatadogClientSearchLogsNormalizesWarningsAndCursor(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/logs/events" {
			t.Fatalf("request path = %q, want %q", r.URL.Path, "/api/v2/logs/events")
		}
		if got := r.URL.Query().Get("filter[query]"); got != "service:api status:error" {
			t.Fatalf("filter[query] = %q, want query", got)
		}
		if got := r.URL.Query().Get("filter[storage_tier]"); got != DatadogLogsStorageTierFlex {
			t.Fatalf("filter[storage_tier] = %q, want %q", got, DatadogLogsStorageTierFlex)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{
				"id":   "log-1",
				"type": "log",
				"attributes": map[string]any{
					"service":   "api",
					"status":    "error",
					"host":      "host-a",
					"message":   "very bad things happened",
					"timestamp": "2026-03-15T00:00:00Z",
					"tags":      []string{"env:prod", "team:core"},
				},
			}},
			"meta": map[string]any{
				"elapsed": 12,
				"status":  "done",
				"page": map[string]any{
					"after": "cursor-2",
				},
				"warnings": []map[string]any{{
					"code":   "partial_results",
					"title":  "Partial results",
					"detail": "Index timeout",
				}},
			},
		})
	}))
	defer server.Close()

	client := newTestDatadogClient(t, server)
	result, err := client.ExecuteRead(context.Background(), DatadogReadRequest{
		Action:      DatadogReadActionSearchLogs,
		Query:       "service:api status:error",
		StorageTier: DatadogLogsStorageTierFlex,
		From:        time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC),
		To:          time.Date(2026, 3, 15, 1, 0, 0, 0, time.UTC),
		PageSize:    10,
	})
	if err != nil {
		t.Fatalf("ExecuteRead(search logs) error = %v", err)
	}
	items, ok := result.Items.([]DatadogLogEntry)
	if !ok {
		t.Fatalf("result.Items type = %T, want []DatadogLogEntry", result.Items)
	}
	if len(items) != 1 || items[0].Service != "api" || items[0].Status != "error" {
		t.Fatalf("items = %#v, want one normalized api error log", items)
	}
	if got := result.NextCursor; got != "cursor-2" {
		t.Fatalf("result.NextCursor = %q, want %q", got, "cursor-2")
	}
	if got := result.ElapsedMS; got != 12 {
		t.Fatalf("result.ElapsedMS = %d, want 12", got)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "partial_results") {
		t.Fatalf("result.Warnings = %#v, want normalized warning", result.Warnings)
	}
}

func TestDatadogClientAggregateLogsNormalizesBucketsAndCursor(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/logs/analytics/aggregate" {
			t.Fatalf("request path = %q, want %q", r.URL.Path, "/api/v2/logs/analytics/aggregate")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("json.NewDecoder(r.Body).Decode() error = %v", err)
		}
		filter, ok := body["filter"].(map[string]any)
		if !ok {
			t.Fatalf("body.filter = %#v, want object", body["filter"])
		}
		if got := filter["query"]; got != "service:api" {
			t.Fatalf("filter.query = %#v, want %q", got, "service:api")
		}
		if got := filter["storage_tier"]; got != DatadogLogsStorageTierFlex {
			t.Fatalf("filter.storage_tier = %#v, want %q", got, DatadogLogsStorageTierFlex)
		}
		indexes, ok := filter["indexes"].([]any)
		if !ok || len(indexes) != 1 || indexes[0] != "main" {
			t.Fatalf("filter.indexes = %#v, want [main]", filter["indexes"])
		}
		compute, ok := body["compute"].([]any)
		if !ok || len(compute) != 2 {
			t.Fatalf("body.compute = %#v, want two compute rules", body["compute"])
		}
		groupBy, ok := body["group_by"].([]any)
		if !ok || len(groupBy) != 1 {
			t.Fatalf("body.group_by = %#v, want one group_by rule", body["group_by"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"buckets": []map[string]any{
					{
						"by": map[string]any{
							"status": "error",
						},
						"computes": map[string]any{
							"c0": 12,
							"c1": 420.5,
						},
					},
					{
						"by": map[string]any{
							"status": "warn",
						},
						"computes": map[string]any{
							"c0": 3,
							"c1": 120.0,
						},
					},
				},
			},
			"meta": map[string]any{
				"elapsed": 15,
				"status":  "done",
				"page": map[string]any{
					"after": "cursor-2",
				},
				"warnings": []map[string]any{{
					"code":   "partial_results",
					"title":  "Partial results",
					"detail": "Index timeout",
				}},
			},
		})
	}))
	defer server.Close()

	client := newTestDatadogClient(t, server)
	result, err := client.ExecuteRead(context.Background(), DatadogReadRequest{
		Action:  DatadogReadActionAggregateLogs,
		Query:   "service:api",
		Indexes: []string{"main"},
		Compute: []DatadogLogsAggregateCompute{
			{
				Aggregation: "count",
				Type:        "total",
			},
			{
				Aggregation: "avg",
				Metric:      "@duration",
				Type:        "total",
			},
		},
		GroupBy: []DatadogLogsAggregateGroupBy{{
			Facet: "status",
			Limit: 5,
			Sort: &DatadogLogsAggregateSort{
				Type:        "measure",
				Order:       "desc",
				Aggregation: "count",
			},
		}},
		StorageTier: DatadogLogsStorageTierFlex,
		From:        time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC),
		To:          time.Date(2026, 3, 15, 1, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("ExecuteRead(aggregate logs) error = %v", err)
	}
	items, ok := result.Items.([]DatadogLogsAggregateBucket)
	if !ok {
		t.Fatalf("result.Items type = %T, want []DatadogLogsAggregateBucket", result.Items)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}
	if got := items[0].By["status"]; got != "error" {
		t.Fatalf("items[0].By[status] = %#v, want %q", got, "error")
	}
	if got := items[0].Computes["c0"]; got != 12.0 {
		t.Fatalf("items[0].Computes[c0] = %#v, want %v", got, 12.0)
	}
	if len(result.Computes) != 2 || result.Computes[0].ID != "c0" || result.Computes[1].ID != "c1" {
		t.Fatalf("result.Computes = %#v, want compute ids c0/c1", result.Computes)
	}
	if len(result.GroupBy) != 1 || result.GroupBy[0].Facet != "status" {
		t.Fatalf("result.GroupBy = %#v, want one status group_by", result.GroupBy)
	}
	if got := result.NextCursor; got != "cursor-2" {
		t.Fatalf("result.NextCursor = %q, want %q", got, "cursor-2")
	}
	if got := result.ElapsedMS; got != 15 {
		t.Fatalf("result.ElapsedMS = %d, want 15", got)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "partial_results") {
		t.Fatalf("result.Warnings = %#v, want normalized warning", result.Warnings)
	}
}

func TestDatadogClientAggregateLogsTruncatesTimeseriesPoints(t *testing.T) {
	t.Parallel()

	points := make([]map[string]any, 0, datadogMaxMetricPoints+5)
	for i := 0; i < datadogMaxMetricPoints+5; i++ {
		points = append(points, map[string]any{
			"time":  time.Date(2026, 3, 15, 0, i, 0, 0, time.UTC).Format(time.RFC3339),
			"value": float64(i),
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/logs/analytics/aggregate" {
			t.Fatalf("request path = %q, want %q", r.URL.Path, "/api/v2/logs/analytics/aggregate")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"buckets": []map[string]any{{
					"computes": map[string]any{
						"c0": points,
					},
				}},
			},
			"meta": map[string]any{
				"status": "done",
			},
		})
	}))
	defer server.Close()

	client := newTestDatadogClient(t, server)
	result, err := client.ExecuteRead(context.Background(), DatadogReadRequest{
		Action: DatadogReadActionAggregateLogs,
		Query:  "service:api",
		Compute: []DatadogLogsAggregateCompute{{
			Aggregation: "count",
			Type:        "timeseries",
			Interval:    "1m",
		}},
		From: time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 3, 15, 1, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("ExecuteRead(aggregate logs timeseries) error = %v", err)
	}
	items, ok := result.Items.([]DatadogLogsAggregateBucket)
	if !ok || len(items) != 1 {
		t.Fatalf("result.Items = %#v, want one aggregate bucket", result.Items)
	}
	series, ok := items[0].Computes["c0"].(DatadogLogsAggregateTimeseries)
	if !ok {
		t.Fatalf("items[0].Computes[c0] type = %T, want DatadogLogsAggregateTimeseries", items[0].Computes["c0"])
	}
	if got := len(series.Points); got != datadogMaxMetricPoints {
		t.Fatalf("len(series.Points) = %d, want %d", got, datadogMaxMetricPoints)
	}
	if !series.Truncated {
		t.Fatal("series.Truncated = false, want true")
	}
	if got := series.Points[0].Time; got != "2026-03-15T00:00:00Z" {
		t.Fatalf("series.Points[0].Time = %q, want first point timestamp", got)
	}
}

func TestDatadogClientQueryMetricsTruncatesPointlists(t *testing.T) {
	t.Parallel()

	pointlist := make([][]any, 0, datadogMaxMetricPoints+5)
	for i := 0; i < datadogMaxMetricPoints+5; i++ {
		pointlist = append(pointlist, []any{1710460800000 + i*60000, float64(i)})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query" {
			t.Fatalf("request path = %q, want %q", r.URL.Path, "/api/v1/query")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":    "ok",
			"from_date": 1710460800000,
			"to_date":   1710464400000,
			"query":     "avg:system.cpu.user{*}",
			"series": []map[string]any{{
				"metric":       "system.cpu.user",
				"display_name": "CPU user",
				"scope":        "host:app-1",
				"interval":     60000,
				"length":       len(pointlist),
				"pointlist":    pointlist,
			}},
		})
	}))
	defer server.Close()

	client := newTestDatadogClient(t, server)
	result, err := client.ExecuteRead(context.Background(), DatadogReadRequest{
		Action: DatadogReadActionQueryMetrics,
		Query:  "avg:system.cpu.user{*}",
		From:   time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC),
		To:     time.Date(2026, 3, 15, 1, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("ExecuteRead(query metrics) error = %v", err)
	}
	if len(result.Series) != 1 {
		t.Fatalf("len(result.Series) = %d, want 1", len(result.Series))
	}
	if got := len(result.Series[0].Points); got != datadogMaxMetricPoints {
		t.Fatalf("len(result.Series[0].Points) = %d, want %d", got, datadogMaxMetricPoints)
	}
	if !result.Series[0].PointsTruncated {
		t.Fatal("result.Series[0].PointsTruncated = false, want true")
	}
}

func newTestDatadogClient(t *testing.T, server *httptest.Server) *DatadogClient {
	t.Helper()

	client, err := NewDatadogClient(DatadogClientConfig{
		APIKey:     "api-key",
		AppKey:     "app-key",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("NewDatadogClient() error = %v", err)
	}
	return client
}
