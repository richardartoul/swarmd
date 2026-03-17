package datadogread

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/richardartoul/swarmd/pkg/server"
	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
)

const (
	toolName               = ToolName
	datadogDefaultPageSize = 25
	datadogMaxPageSize     = 50
	datadogDefaultLookback = time.Hour
)

const (
	DefaultPageSize = datadogDefaultPageSize
	DefaultLookback = datadogDefaultLookback
)

var registerOnce sync.Once

type input struct {
	Action      string `json:"action"`
	IncidentID  string `json:"incident_id,omitempty"`
	MonitorID   *int64 `json:"monitor_id,omitempty"`
	DashboardID string `json:"dashboard_id,omitempty"`
	Query       string `json:"query,omitempty"`
	From        string `json:"from,omitempty"`
	To          string `json:"to,omitempty"`
	Page        *int64 `json:"page,omitempty"`
	PageSize    *int   `json:"page_size,omitempty"`
	PageOffset  *int64 `json:"page_offset,omitempty"`
	PageCursor  string `json:"page_cursor,omitempty"`
}

type Input = input

type plugin struct {
	host server.ToolHost
}

func init() {
	Register()
}

func Register() {
	registerOnce.Do(func() {
		server.RegisterTool(func(host server.ToolHost) toolscore.ToolPlugin {
			return plugin{host: host}
		}, server.ToolRegistrationOptions{
			RequiredEnv: []string{DatadogAPIKeyEnvVar, DatadogAppKeyEnvVar},
		})
	})
}

func (plugin) Definition() toolscore.ToolDefinition {
	return toolscore.ToolDefinition{
		Name:        toolName,
		Description: "Read incidents, monitors, dashboards, metrics, logs, and events from Datadog through the tool-owned Datadog client.",
		Kind:        toolscore.ToolKindFunction,
		Parameters: toolscommon.ObjectSchema(
			map[string]any{
				"action": toolscommon.StringEnumSchema(
					"Datadog read action to execute.",
					DatadogReadActionListIncidents,
					DatadogReadActionGetIncident,
					DatadogReadActionListMonitors,
					DatadogReadActionGetMonitor,
					DatadogReadActionListDashboards,
					DatadogReadActionGetDashboard,
					DatadogReadActionQueryMetrics,
					DatadogReadActionSearchLogs,
					DatadogReadActionListEvents,
				),
				"incident_id": toolscommon.StringSchema(`Required for "get_incident".`),
				"monitor_id":  toolscommon.IntegerSchema(`Required for "get_monitor".`),
				"dashboard_id": toolscommon.StringSchema(
					`Required for "get_dashboard".`,
				),
				"query": toolscommon.StringSchema(
					`Required for "query_metrics" and "search_logs". Optional name filter for "list_monitors".`,
				),
				"from": toolscommon.StringSchema(
					`Optional RFC3339 timestamp or Unix seconds. Used by "query_metrics", "search_logs", and "list_events". Defaults to one hour before "to".`,
				),
				"to": toolscommon.StringSchema(
					`Optional RFC3339 timestamp or Unix seconds. Used by "query_metrics", "search_logs", and "list_events". Defaults to the current time.`,
				),
				"page": toolscommon.IntegerSchema(
					`Optional page number for "list_monitors" or "list_events".`,
				),
				"page_size": toolscommon.IntegerSchema(
					`Optional page size for list actions and log search. Values above 50 are capped.`,
				),
				"page_offset": toolscommon.IntegerSchema(
					`Optional offset for "list_incidents" and "list_dashboards".`,
				),
				"page_cursor": toolscommon.StringSchema(
					`Optional Datadog cursor for "search_logs".`,
				),
			},
			"action",
		),
		RequiredArguments: []string{"action"},
		Examples: []string{
			`{"action":"list_incidents","page_size":10}`,
			`{"action":"query_metrics","query":"avg:system.cpu.user{*}","from":"2026-03-15T00:00:00Z","to":"2026-03-15T01:00:00Z"}`,
			`{"action":"search_logs","query":"service:api status:error","from":"2026-03-15T00:00:00Z","to":"2026-03-15T01:00:00Z","page_size":20}`,
		},
		OutputNotes:     "Returns bounded, normalized JSON for the requested Datadog resource type.",
		SafetyTags:      []string{"network", "read_only"},
		RequiresNetwork: true,
		ReadOnly:        true,
	}
}

func (p plugin) NewHandler(config toolscore.ConfiguredTool) (toolscore.ToolHandler, error) {
	if err := toolscommon.ValidateNoToolConfig(toolName, config.Config); err != nil {
		return nil, err
	}
	return toolscore.ToolHandlerFunc(p.handle), nil
}

func (p plugin) handle(ctx context.Context, toolCtx toolscore.ToolContext, step *toolscore.Step, call *toolscore.ToolAction) error {
	input, err := toolscore.DecodeToolInput[input](call.Input)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	if !p.host.NetworkEnabled(toolCtx) {
		toolCtx.SetPolicyError(step, fmt.Errorf("%s requires network.reachable_hosts to be configured", toolName))
		return nil
	}
	req, err := normalizeReadRequest(input, time.Now().UTC())
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	runtime, err := p.host.Runtime(toolCtx)
	if err != nil {
		return err
	}
	service, err := datadogServiceForToolCall(runtime, p.host.HTTPClient(toolCtx, toolscore.ToolHTTPClientOptions{
		ConnectTimeout:  10 * time.Second,
		FollowRedirects: true,
	}))
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	result, err := service.ExecuteRead(ctx, req)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	output, err := toolscommon.MarshalToolOutput(result)
	if err != nil {
		return err
	}
	toolCtx.SetOutput(step, output)
	return nil
}

func datadogServiceForToolCall(runtime server.ToolRuntime, httpClient *http.Client) (DatadogReadService, error) {
	client, err := NewDatadogClient(DatadogClientConfig{
		APIKey:     runtime.LookupEnv(DatadogAPIKeyEnvVar),
		AppKey:     runtime.LookupEnv(DatadogAppKeyEnvVar),
		Site:       runtime.LookupEnv(DatadogSiteEnvVar),
		BaseURL:    runtime.LookupEnv(datadogBaseURLEnvVar),
		HTTPClient: httpClient,
	})
	if err != nil {
		return nil, err
	}
	return client, nil
}

func normalizeReadRequest(input input, now time.Time) (DatadogReadRequest, error) {
	action := strings.TrimSpace(input.Action)
	if action == "" {
		return DatadogReadRequest{}, fmt.Errorf("action must not be empty")
	}
	req := DatadogReadRequest{
		Action:      action,
		IncidentID:  strings.TrimSpace(input.IncidentID),
		DashboardID: strings.TrimSpace(input.DashboardID),
		Query:       strings.TrimSpace(input.Query),
		PageCursor:  strings.TrimSpace(input.PageCursor),
	}
	if input.MonitorID != nil {
		req.MonitorID = *input.MonitorID
	}
	if input.Page != nil {
		req.Page = *input.Page
	}
	if input.PageSize != nil {
		req.PageSize = *input.PageSize
	}
	if input.PageOffset != nil {
		req.PageOffset = *input.PageOffset
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	switch action {
	case DatadogReadActionListIncidents:
		if err := rejectUnexpectedFields(action, input, "page_size", "page_offset"); err != nil {
			return DatadogReadRequest{}, err
		}
		size, err := normalizePageSize(req.PageSize)
		if err != nil {
			return DatadogReadRequest{}, err
		}
		if req.PageOffset < 0 {
			return DatadogReadRequest{}, fmt.Errorf("page_offset must be >= 0")
		}
		req.PageSize = size
		return req, nil
	case DatadogReadActionGetIncident:
		if err := rejectUnexpectedFields(action, input, "incident_id"); err != nil {
			return DatadogReadRequest{}, err
		}
		if req.IncidentID == "" {
			return DatadogReadRequest{}, fmt.Errorf("incident_id must not be empty")
		}
		return req, nil
	case DatadogReadActionListMonitors:
		if err := rejectUnexpectedFields(action, input, "query", "page", "page_size"); err != nil {
			return DatadogReadRequest{}, err
		}
		size, err := normalizePageSize(req.PageSize)
		if err != nil {
			return DatadogReadRequest{}, err
		}
		if req.Page < 0 {
			return DatadogReadRequest{}, fmt.Errorf("page must be >= 0")
		}
		req.PageSize = size
		return req, nil
	case DatadogReadActionGetMonitor:
		if err := rejectUnexpectedFields(action, input, "monitor_id"); err != nil {
			return DatadogReadRequest{}, err
		}
		if input.MonitorID == nil || req.MonitorID <= 0 {
			return DatadogReadRequest{}, fmt.Errorf("monitor_id must be a positive integer")
		}
		return req, nil
	case DatadogReadActionListDashboards:
		if err := rejectUnexpectedFields(action, input, "page_size", "page_offset"); err != nil {
			return DatadogReadRequest{}, err
		}
		size, err := normalizePageSize(req.PageSize)
		if err != nil {
			return DatadogReadRequest{}, err
		}
		if req.PageOffset < 0 {
			return DatadogReadRequest{}, fmt.Errorf("page_offset must be >= 0")
		}
		req.PageSize = size
		return req, nil
	case DatadogReadActionGetDashboard:
		if err := rejectUnexpectedFields(action, input, "dashboard_id"); err != nil {
			return DatadogReadRequest{}, err
		}
		if req.DashboardID == "" {
			return DatadogReadRequest{}, fmt.Errorf("dashboard_id must not be empty")
		}
		return req, nil
	case DatadogReadActionQueryMetrics:
		if err := rejectUnexpectedFields(action, input, "query", "from", "to"); err != nil {
			return DatadogReadRequest{}, err
		}
		if req.Query == "" {
			return DatadogReadRequest{}, fmt.Errorf("query must not be empty")
		}
		from, to, err := resolveTimeWindow(input.From, input.To, now)
		if err != nil {
			return DatadogReadRequest{}, err
		}
		req.From = from
		req.To = to
		return req, nil
	case DatadogReadActionSearchLogs:
		if err := rejectUnexpectedFields(action, input, "query", "from", "to", "page_size", "page_cursor"); err != nil {
			return DatadogReadRequest{}, err
		}
		if req.Query == "" {
			return DatadogReadRequest{}, fmt.Errorf("query must not be empty")
		}
		size, err := normalizePageSize(req.PageSize)
		if err != nil {
			return DatadogReadRequest{}, err
		}
		from, to, err := resolveTimeWindow(input.From, input.To, now)
		if err != nil {
			return DatadogReadRequest{}, err
		}
		req.PageSize = size
		req.From = from
		req.To = to
		return req, nil
	case DatadogReadActionListEvents:
		if err := rejectUnexpectedFields(action, input, "from", "to", "page"); err != nil {
			return DatadogReadRequest{}, err
		}
		if req.Page < 0 {
			return DatadogReadRequest{}, fmt.Errorf("page must be >= 0")
		}
		from, to, err := resolveTimeWindow(input.From, input.To, now)
		if err != nil {
			return DatadogReadRequest{}, err
		}
		req.From = from
		req.To = to
		return req, nil
	default:
		return DatadogReadRequest{}, fmt.Errorf("unsupported action %q", action)
	}
}

func rejectUnexpectedFields(action string, input input, allowed ...string) error {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		allowedSet[name] = struct{}{}
	}
	present := map[string]bool{
		"incident_id":  strings.TrimSpace(input.IncidentID) != "",
		"monitor_id":   input.MonitorID != nil,
		"dashboard_id": strings.TrimSpace(input.DashboardID) != "",
		"query":        strings.TrimSpace(input.Query) != "",
		"from":         strings.TrimSpace(input.From) != "",
		"to":           strings.TrimSpace(input.To) != "",
		"page":         input.Page != nil,
		"page_size":    input.PageSize != nil,
		"page_offset":  input.PageOffset != nil,
		"page_cursor":  strings.TrimSpace(input.PageCursor) != "",
	}
	unexpected := make([]string, 0)
	for name, isPresent := range present {
		if !isPresent {
			continue
		}
		if _, ok := allowedSet[name]; ok {
			continue
		}
		unexpected = append(unexpected, name)
	}
	if len(unexpected) == 0 {
		return nil
	}
	sort.Strings(unexpected)
	return fmt.Errorf("%s does not accept %s", action, strings.Join(unexpected, ", "))
}

func normalizePageSize(pageSize int) (int, error) {
	if pageSize == 0 {
		return datadogDefaultPageSize, nil
	}
	if pageSize < 0 {
		return 0, fmt.Errorf("page_size must be > 0")
	}
	if pageSize > datadogMaxPageSize {
		return datadogMaxPageSize, nil
	}
	return pageSize, nil
}

func resolveTimeWindow(fromRaw, toRaw string, now time.Time) (time.Time, time.Time, error) {
	to := now.UTC()
	if strings.TrimSpace(toRaw) != "" {
		parsed, err := parseTimestamp(toRaw)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("to: %w", err)
		}
		to = parsed
	}
	from := to.Add(-datadogDefaultLookback)
	if strings.TrimSpace(fromRaw) != "" {
		parsed, err := parseTimestamp(fromRaw)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("from: %w", err)
		}
		from = parsed
	}
	if !from.Before(to) {
		return time.Time{}, time.Time{}, fmt.Errorf("from must be before to")
	}
	return from, to, nil
}

func parseTimestamp(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, fmt.Errorf("timestamp must not be empty")
	}
	if unixSeconds, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.Unix(unixSeconds, 0).UTC(), nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("must be RFC3339 or Unix seconds")
	}
	return parsed.UTC(), nil
}

func NormalizeReadRequest(value Input, now time.Time) (DatadogReadRequest, error) {
	return normalizeReadRequest(value, now)
}
