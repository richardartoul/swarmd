package datadogread

import (
	"context"
	"net/http"
	"time"
)

const (
	ToolName = "datadog_read"

	DatadogReadActionListIncidents  = "list_incidents"
	DatadogReadActionGetIncident    = "get_incident"
	DatadogReadActionListMonitors   = "list_monitors"
	DatadogReadActionGetMonitor     = "get_monitor"
	DatadogReadActionListDashboards = "list_dashboards"
	DatadogReadActionGetDashboard   = "get_dashboard"
	DatadogReadActionQueryMetrics   = "query_metrics"
	DatadogReadActionSearchLogs     = "search_logs"
	DatadogReadActionListEvents     = "list_events"

	DatadogAPIKeyEnvVar = "DD_API_KEY"
	DatadogAppKeyEnvVar = "DD_APP_KEY"
	DatadogSiteEnvVar   = "DD_SITE"

	datadogBaseURLEnvVar = "DD_BASE_URL"
)

type DatadogReadService interface {
	ExecuteRead(ctx context.Context, req DatadogReadRequest) (DatadogReadResult, error)
}

type DatadogHTTPClientConfigurer interface {
	WithHTTPClient(httpClient *http.Client) (DatadogReadService, error)
}

type DatadogReadRequest struct {
	Action      string
	IncidentID  string
	MonitorID   int64
	DashboardID string
	Query       string
	From        time.Time
	To          time.Time
	Page        int64
	PageSize    int
	PageOffset  int64
	PageCursor  string
}

type DatadogReadResult struct {
	Action          string                `json:"action"`
	Item            any                   `json:"item,omitempty"`
	Items           any                   `json:"items,omitempty"`
	Series          []DatadogMetricSeries `json:"series,omitempty"`
	Query           string                `json:"query,omitempty"`
	From            string                `json:"from,omitempty"`
	To              string                `json:"to,omitempty"`
	Page            int64                 `json:"page,omitempty"`
	PageSize        int                   `json:"page_size,omitempty"`
	PageOffset      int64                 `json:"page_offset,omitempty"`
	NextOffset      *int64                `json:"next_offset,omitempty"`
	NextCursor      string                `json:"next_cursor,omitempty"`
	Status          string                `json:"status,omitempty"`
	ElapsedMS       int64                 `json:"elapsed_ms,omitempty"`
	Warnings        []string              `json:"warnings,omitempty"`
	SeriesTruncated bool                  `json:"series_truncated,omitempty"`
}

type DatadogIncident struct {
	ID                  string `json:"id"`
	PublicID            int64  `json:"public_id,omitempty"`
	Title               string `json:"title"`
	Severity            string `json:"severity,omitempty"`
	State               string `json:"state,omitempty"`
	CreatedAt           string `json:"created_at,omitempty"`
	ModifiedAt          string `json:"modified_at,omitempty"`
	DeclaredAt          string `json:"declared_at,omitempty"`
	DetectedAt          string `json:"detected_at,omitempty"`
	ResolvedAt          string `json:"resolved_at,omitempty"`
	CustomerImpacted    *bool  `json:"customer_impacted,omitempty"`
	CustomerImpactScope string `json:"customer_impact_scope,omitempty"`
	IncidentTypeUUID    string `json:"incident_type_uuid,omitempty"`
	IsTest              *bool  `json:"is_test,omitempty"`
}

type DatadogMonitor struct {
	ID           int64    `json:"id"`
	Name         string   `json:"name,omitempty"`
	Type         string   `json:"type,omitempty"`
	OverallState string   `json:"overall_state,omitempty"`
	Query        string   `json:"query"`
	Message      string   `json:"message,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	Priority     *int64   `json:"priority,omitempty"`
	CreatedAt    string   `json:"created_at,omitempty"`
	ModifiedAt   string   `json:"modified_at,omitempty"`
	Multi        *bool    `json:"multi,omitempty"`
}

type DatadogDashboard struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	LayoutType   string `json:"layout_type,omitempty"`
	Description  string `json:"description,omitempty"`
	AuthorHandle string `json:"author_handle,omitempty"`
	URL          string `json:"url,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
	ModifiedAt   string `json:"modified_at,omitempty"`
	IsReadOnly   *bool  `json:"is_read_only,omitempty"`
	WidgetCount  int    `json:"widget_count,omitempty"`
}

type DatadogMetricSeries struct {
	Metric          string               `json:"metric,omitempty"`
	DisplayName     string               `json:"display_name,omitempty"`
	Expression      string               `json:"expression,omitempty"`
	Scope           string               `json:"scope,omitempty"`
	Aggregation     string               `json:"aggregation,omitempty"`
	QueryIndex      int64                `json:"query_index,omitempty"`
	IntervalMS      int64                `json:"interval_ms,omitempty"`
	Length          int64                `json:"length,omitempty"`
	StartMS         int64                `json:"start_ms,omitempty"`
	EndMS           int64                `json:"end_ms,omitempty"`
	Tags            []string             `json:"tags,omitempty"`
	Points          []DatadogMetricPoint `json:"points,omitempty"`
	PointsTruncated bool                 `json:"points_truncated,omitempty"`
}

type DatadogMetricPoint struct {
	TimestampMS int64   `json:"timestamp_ms"`
	Value       float64 `json:"value"`
}

type DatadogLogEntry struct {
	ID        string   `json:"id,omitempty"`
	Timestamp string   `json:"timestamp,omitempty"`
	Service   string   `json:"service,omitempty"`
	Status    string   `json:"status,omitempty"`
	Host      string   `json:"host,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Message   string   `json:"message,omitempty"`
}

type DatadogEvent struct {
	ID             string   `json:"id,omitempty"`
	Title          string   `json:"title,omitempty"`
	Text           string   `json:"text,omitempty"`
	AlertType      string   `json:"alert_type,omitempty"`
	Priority       string   `json:"priority,omitempty"`
	SourceTypeName string   `json:"source_type_name,omitempty"`
	Host           string   `json:"host,omitempty"`
	URL            string   `json:"url,omitempty"`
	DateHappened   string   `json:"date_happened,omitempty"`
	Tags           []string `json:"tags,omitempty"`
}
