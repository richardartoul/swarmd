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
	toolName                          = ToolName
	datadogDefaultPageSize            = 25
	datadogMaxPageSize                = 50
	datadogDefaultLookback            = time.Hour
	datadogDefaultAggregateGroupLimit = 10
	datadogMaxAggregateGroupLimit     = 20
	datadogMaxAggregateComputes       = 3
	datadogMaxAggregateGroupBy        = 3
	datadogMaxAggregateBucketProduct  = 100
	datadogMaxAggregateIndexes        = 10
)

const (
	DefaultPageSize = datadogDefaultPageSize
	DefaultLookback = datadogDefaultLookback
)

var registerOnce sync.Once

var (
	datadogLogsAggregationFunctions = []string{
		"count",
		"cardinality",
		"pc75",
		"pc90",
		"pc95",
		"pc98",
		"pc99",
		"sum",
		"min",
		"max",
		"avg",
		"median",
	}
	datadogLogsComputeTypes = []string{
		"total",
		"timeseries",
	}
	datadogLogsAggregateSortTypes = []string{
		"alphabetical",
		"measure",
	}
	datadogLogsSortOrders = []string{
		"asc",
		"desc",
	}
)

type input struct {
	Action      string                      `json:"action"`
	IncidentID  string                      `json:"incident_id,omitempty"`
	MonitorID   *int64                      `json:"monitor_id,omitempty"`
	DashboardID string                      `json:"dashboard_id,omitempty"`
	Query       string                      `json:"query,omitempty"`
	Indexes     []string                    `json:"indexes,omitempty"`
	Compute     []logsAggregateComputeInput `json:"compute,omitempty"`
	GroupBy     []logsAggregateGroupByInput `json:"group_by,omitempty"`
	StorageTier string                      `json:"storage_tier,omitempty"`
	From        string                      `json:"from,omitempty"`
	To          string                      `json:"to,omitempty"`
	Page        *int64                      `json:"page,omitempty"`
	PageSize    *int                        `json:"page_size,omitempty"`
	PageOffset  *int64                      `json:"page_offset,omitempty"`
	PageCursor  string                      `json:"page_cursor,omitempty"`
}

type logsAggregateComputeInput struct {
	Aggregation string `json:"aggregation"`
	Metric      string `json:"metric,omitempty"`
	Type        string `json:"type,omitempty"`
	Interval    string `json:"interval,omitempty"`
}

type logsAggregateGroupByInput struct {
	Facet string                  `json:"facet"`
	Limit *int64                  `json:"limit,omitempty"`
	Sort  *logsAggregateSortInput `json:"sort,omitempty"`
}

type logsAggregateSortInput struct {
	Order       string `json:"order,omitempty"`
	Type        string `json:"type,omitempty"`
	Aggregation string `json:"aggregation,omitempty"`
	Metric      string `json:"metric,omitempty"`
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
			RequiredEnv:   []string{DatadogAPIKeyEnvVar, DatadogAppKeyEnvVar},
			RequiredHosts: RequiredHosts(),
		})
	})
}

func (plugin) Definition() toolscore.ToolDefinition {
	return toolscore.ToolDefinition{
		Name:        toolName,
		Description: "Read incidents, monitors, dashboards, metrics, log search results, log aggregates, and events from Datadog through the tool-owned Datadog client.",
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
					DatadogReadActionAggregateLogs,
					DatadogReadActionListEvents,
				),
				"incident_id": toolscommon.StringSchema(`Required for "get_incident".`),
				"monitor_id":  toolscommon.IntegerSchema(`Required for "get_monitor".`),
				"dashboard_id": toolscommon.StringSchema(
					`Required for "get_dashboard".`,
				),
				"query": toolscommon.StringSchema(
					`Required for "query_metrics", "search_logs", and "aggregate_logs". Optional name filter for "list_monitors".`,
				),
				"indexes": stringArraySchema(
					`Optional list of Datadog log indexes for "aggregate_logs".`,
				),
				"compute": arraySchema(
					`Optional log aggregation computes for "aggregate_logs". Defaults to [{"aggregation":"count","type":"total"}] when omitted.`,
					toolscommon.ObjectSchema(
						map[string]any{
							"aggregation": toolscommon.StringEnumSchema(
								`Aggregation function for this compute.`,
								datadogLogsAggregationFunctions...,
							),
							"metric": toolscommon.StringSchema(
								`Optional measure attribute like "@duration". Required for aggregations other than "count".`,
							),
							"type": toolscommon.StringEnumSchema(
								`Optional compute type for "aggregate_logs". Use "total" or "timeseries". Defaults to "total".`,
								datadogLogsComputeTypes...,
							),
							"interval": toolscommon.StringSchema(
								`Optional rollup interval like "1m" for timeseries computes.`,
							),
						},
						"aggregation",
					),
				),
				"group_by": arraySchema(
					`Optional group-by rules for "aggregate_logs". Each rule groups by one facet and can set its own limit and sort.`,
					toolscommon.ObjectSchema(
						map[string]any{
							"facet": toolscommon.StringSchema(
								`Facet to group by, such as "service" or "@http.status_code".`,
							),
							"limit": toolscommon.IntegerSchema(
								`Optional max buckets for this facet. Defaults to 10 and caps at 20.`,
							),
							"sort": toolscommon.ObjectSchema(
								map[string]any{
									"order": toolscommon.StringEnumSchema(
										`Optional sort order. Use "asc" or "desc".`,
										datadogLogsSortOrders...,
									),
									"type": toolscommon.StringEnumSchema(
										`Optional sort type. Use "alphabetical" or "measure". Defaults to "alphabetical" when only "order" is set, otherwise "measure".`,
										datadogLogsAggregateSortTypes...,
									),
									"aggregation": toolscommon.StringEnumSchema(
										`Optional aggregation to sort by for measure sorts. Defaults to the first compute when there is exactly one.`,
										datadogLogsAggregationFunctions...,
									),
									"metric": toolscommon.StringSchema(
										`Optional metric to sort by for measure sorts such as "@duration".`,
									),
								},
							),
						},
						"facet",
					),
				),
				"storage_tier": toolscommon.StringEnumSchema(
					`Optional storage tier for "search_logs" or "aggregate_logs". Use "indexes", "online-archives", or "flex".`,
					DatadogLogsStorageTierIndexes,
					DatadogLogsStorageTierOnlineArchives,
					DatadogLogsStorageTierFlex,
				),
				"from": toolscommon.StringSchema(
					`Optional RFC3339 timestamp or Unix seconds. Used by "query_metrics", "search_logs", "aggregate_logs", and "list_events". Defaults to one hour before "to".`,
				),
				"to": toolscommon.StringSchema(
					`Optional RFC3339 timestamp or Unix seconds. Used by "query_metrics", "search_logs", "aggregate_logs", and "list_events". Defaults to the current time.`,
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
					`Optional Datadog cursor for "search_logs" or "aggregate_logs".`,
				),
			},
			"action",
		),
		RequiredArguments: []string{"action"},
		Examples: []string{
			`{"action":"list_incidents","page_size":10}`,
			`{"action":"query_metrics","query":"avg:system.cpu.user{*}","from":"2026-03-15T00:00:00Z","to":"2026-03-15T01:00:00Z"}`,
			`{"action":"search_logs","query":"service:api status:error","from":"2026-03-15T00:00:00Z","to":"2026-03-15T01:00:00Z","page_size":20}`,
			`{"action":"search_logs","query":"service:api status:error","storage_tier":"flex","from":"2026-03-15T00:00:00Z","to":"2026-03-15T01:00:00Z","page_size":20}`,
			`{"action":"aggregate_logs","query":"service:api","compute":[{"aggregation":"count"}],"group_by":[{"facet":"status","limit":5,"sort":{"aggregation":"count","order":"desc"}}],"from":"2026-03-15T00:00:00Z","to":"2026-03-15T01:00:00Z"}`,
			`{"action":"aggregate_logs","query":"service:web","compute":[{"aggregation":"avg","metric":"@duration","type":"timeseries","interval":"5m"}],"group_by":[{"facet":"service","limit":3,"sort":{"aggregation":"avg","metric":"@duration","order":"desc"}}],"from":"2026-03-15T00:00:00Z","to":"2026-03-15T01:00:00Z"}`,
		},
		OutputNotes:  "Returns bounded, normalized JSON for the requested Datadog resource type.",
		SafetyTags:   []string{"network", "read_only"},
		NetworkScope: toolscore.ToolNetworkScopeScoped,
		ReadOnly:     true,
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
		Indexes:     append([]string(nil), input.Indexes...),
		StorageTier: strings.TrimSpace(input.StorageTier),
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
		if err := rejectUnexpectedFields(action, input, "query", "storage_tier", "from", "to", "page_size", "page_cursor"); err != nil {
			return DatadogReadRequest{}, err
		}
		if req.Query == "" {
			return DatadogReadRequest{}, fmt.Errorf("query must not be empty")
		}
		size, err := normalizePageSize(req.PageSize)
		if err != nil {
			return DatadogReadRequest{}, err
		}
		storageTier, err := normalizeLogsStorageTier(req.StorageTier)
		if err != nil {
			return DatadogReadRequest{}, err
		}
		from, to, err := resolveTimeWindow(input.From, input.To, now)
		if err != nil {
			return DatadogReadRequest{}, err
		}
		req.PageSize = size
		req.StorageTier = storageTier
		req.From = from
		req.To = to
		return req, nil
	case DatadogReadActionAggregateLogs:
		if err := rejectUnexpectedFields(action, input, "query", "indexes", "compute", "group_by", "storage_tier", "from", "to", "page_cursor"); err != nil {
			return DatadogReadRequest{}, err
		}
		if req.Query == "" {
			return DatadogReadRequest{}, fmt.Errorf("query must not be empty")
		}
		indexes, err := normalizeAggregateIndexes(input.Indexes)
		if err != nil {
			return DatadogReadRequest{}, err
		}
		compute, err := normalizeLogsAggregateComputes(input.Compute)
		if err != nil {
			return DatadogReadRequest{}, err
		}
		groupBy, err := normalizeLogsAggregateGroupBy(input.GroupBy, compute)
		if err != nil {
			return DatadogReadRequest{}, err
		}
		storageTier, err := normalizeLogsStorageTier(req.StorageTier)
		if err != nil {
			return DatadogReadRequest{}, err
		}
		from, to, err := resolveTimeWindow(input.From, input.To, now)
		if err != nil {
			return DatadogReadRequest{}, err
		}
		if req.PageCursor != "" {
			if len(groupBy) == 0 {
				return DatadogReadRequest{}, fmt.Errorf("page_cursor requires at least one group_by rule")
			}
			if !hasAlphabeticalAggregateSort(groupBy) {
				return DatadogReadRequest{}, fmt.Errorf("page_cursor requires at least one alphabetical group_by sort")
			}
		}
		req.Indexes = indexes
		req.Compute = compute
		req.GroupBy = groupBy
		req.StorageTier = storageTier
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
		"indexes":      len(input.Indexes) > 0,
		"compute":      len(input.Compute) > 0,
		"group_by":     len(input.GroupBy) > 0,
		"storage_tier": strings.TrimSpace(input.StorageTier) != "",
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

func normalizeLogsStorageTier(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "":
		return "", nil
	case DatadogLogsStorageTierIndexes, DatadogLogsStorageTierOnlineArchives, DatadogLogsStorageTierFlex:
		return value, nil
	default:
		return "", fmt.Errorf(
			"storage_tier must be one of %q, %q, or %q",
			DatadogLogsStorageTierIndexes,
			DatadogLogsStorageTierOnlineArchives,
			DatadogLogsStorageTierFlex,
		)
	}
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

func normalizeAggregateIndexes(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	indexes := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, index := range raw {
		index = strings.TrimSpace(index)
		if index == "" {
			continue
		}
		if _, ok := seen[index]; ok {
			continue
		}
		seen[index] = struct{}{}
		indexes = append(indexes, index)
		if len(indexes) > datadogMaxAggregateIndexes {
			return nil, fmt.Errorf("indexes accepts at most %d entries", datadogMaxAggregateIndexes)
		}
	}
	if len(indexes) == 0 {
		return nil, nil
	}
	return indexes, nil
}

func normalizeLogsAggregateComputes(raw []logsAggregateComputeInput) ([]DatadogLogsAggregateCompute, error) {
	if len(raw) == 0 {
		return []DatadogLogsAggregateCompute{{
			Aggregation: "count",
			Type:        "total",
		}}, nil
	}
	if len(raw) > datadogMaxAggregateComputes {
		return nil, fmt.Errorf("compute accepts at most %d entries", datadogMaxAggregateComputes)
	}
	compute := make([]DatadogLogsAggregateCompute, 0, len(raw))
	for idx, item := range raw {
		fieldPrefix := fmt.Sprintf("compute[%d]", idx)
		aggregation, err := normalizeLogsAggregationFunction(item.Aggregation, fieldPrefix+".aggregation")
		if err != nil {
			return nil, err
		}
		computeType, err := normalizeLogsComputeType(item.Type, fieldPrefix+".type")
		if err != nil {
			return nil, err
		}
		if computeType == "" {
			computeType = "total"
		}
		metric := strings.TrimSpace(item.Metric)
		interval := strings.TrimSpace(item.Interval)
		if aggregation == "count" {
			if metric != "" {
				return nil, fmt.Errorf("%s.metric is not allowed for count aggregation", fieldPrefix)
			}
		} else if metric == "" {
			return nil, fmt.Errorf("%s.metric must not be empty for %s aggregation", fieldPrefix, aggregation)
		}
		if computeType != "timeseries" && interval != "" {
			return nil, fmt.Errorf("%s.interval is only supported for timeseries computes", fieldPrefix)
		}
		compute = append(compute, DatadogLogsAggregateCompute{
			Aggregation: aggregation,
			Metric:      metric,
			Type:        computeType,
			Interval:    interval,
		})
	}
	return compute, nil
}

func normalizeLogsAggregateGroupBy(raw []logsAggregateGroupByInput, compute []DatadogLogsAggregateCompute) ([]DatadogLogsAggregateGroupBy, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > datadogMaxAggregateGroupBy {
		return nil, fmt.Errorf("group_by accepts at most %d entries", datadogMaxAggregateGroupBy)
	}
	groupBy := make([]DatadogLogsAggregateGroupBy, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	bucketProduct := 1
	for idx, item := range raw {
		fieldPrefix := fmt.Sprintf("group_by[%d]", idx)
		facet := strings.TrimSpace(item.Facet)
		if facet == "" {
			return nil, fmt.Errorf("%s.facet must not be empty", fieldPrefix)
		}
		if _, ok := seen[facet]; ok {
			return nil, fmt.Errorf("%s.facet duplicates %q", fieldPrefix, facet)
		}
		seen[facet] = struct{}{}
		limit := int64(datadogDefaultAggregateGroupLimit)
		if item.Limit != nil {
			limit = *item.Limit
		}
		if limit <= 0 {
			limit = datadogDefaultAggregateGroupLimit
		}
		if limit > datadogMaxAggregateGroupLimit {
			limit = datadogMaxAggregateGroupLimit
		}
		if bucketProduct > datadogMaxAggregateBucketProduct/int(limit) {
			return nil, fmt.Errorf("group_by bucket product must not exceed %d", datadogMaxAggregateBucketProduct)
		}
		bucketProduct *= int(limit)
		sortRule, err := normalizeLogsAggregateSort(item.Sort, fieldPrefix+".sort", compute)
		if err != nil {
			return nil, err
		}
		groupBy = append(groupBy, DatadogLogsAggregateGroupBy{
			Facet: facet,
			Limit: limit,
			Sort:  sortRule,
		})
	}
	return groupBy, nil
}

func normalizeLogsAggregateSort(raw *logsAggregateSortInput, field string, compute []DatadogLogsAggregateCompute) (*DatadogLogsAggregateSort, error) {
	if raw == nil {
		return nil, nil
	}
	order := strings.ToLower(strings.TrimSpace(raw.Order))
	sortType := strings.ToLower(strings.TrimSpace(raw.Type))
	aggregation := strings.ToLower(strings.TrimSpace(raw.Aggregation))
	metric := strings.TrimSpace(raw.Metric)
	if order == "" && sortType == "" && aggregation == "" && metric == "" {
		return nil, nil
	}
	if sortType == "" {
		if aggregation != "" || metric != "" {
			sortType = "measure"
		} else {
			sortType = "alphabetical"
		}
	}
	sortType, err := normalizeLogsAggregateSortType(sortType, field+".type")
	if err != nil {
		return nil, err
	}
	switch sortType {
	case "alphabetical":
		if aggregation != "" || metric != "" {
			return nil, fmt.Errorf("%s.aggregation and %s.metric are only valid for measure sorts", field, field)
		}
		if order == "" {
			order = "asc"
		}
	case "measure":
		if len(compute) != 1 && aggregation == "" {
			return nil, fmt.Errorf("%s.aggregation is required when multiple compute rules are configured", field)
		}
		if aggregation == "" {
			aggregation = compute[0].Aggregation
		}
		aggregation, err = normalizeLogsAggregationFunction(aggregation, field+".aggregation")
		if err != nil {
			return nil, err
		}
		if aggregation == "count" {
			if metric != "" {
				return nil, fmt.Errorf("%s.metric is not allowed for count aggregation", field)
			}
		} else if metric == "" {
			if len(compute) == 1 && compute[0].Aggregation == aggregation && compute[0].Metric != "" {
				metric = compute[0].Metric
			} else {
				return nil, fmt.Errorf("%s.metric must not be empty for %s aggregation", field, aggregation)
			}
		}
		if order == "" {
			order = "desc"
		}
	default:
		return nil, fmt.Errorf("%s.type must not be empty", field)
	}
	order, err = normalizeLogsSortOrder(order, field+".order")
	if err != nil {
		return nil, err
	}
	result := &DatadogLogsAggregateSort{
		Order: order,
		Type:  sortType,
	}
	if sortType == "measure" {
		result.Aggregation = aggregation
		result.Metric = metric
	}
	return result, nil
}

func hasAlphabeticalAggregateSort(groupBy []DatadogLogsAggregateGroupBy) bool {
	for _, rule := range groupBy {
		if rule.Sort != nil && rule.Sort.Type == "alphabetical" {
			return true
		}
	}
	return false
}

func normalizeLogsAggregationFunction(raw, field string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "count", "cardinality", "pc75", "pc90", "pc95", "pc98", "pc99", "sum", "min", "max", "avg", "median":
		return value, nil
	case "":
		return "", fmt.Errorf("%s must not be empty", field)
	default:
		return "", fmt.Errorf("%s must be one of %q, %q, %q, %q, %q, %q, %q, %q, %q, %q, %q, or %q",
			field,
			"count",
			"cardinality",
			"pc75",
			"pc90",
			"pc95",
			"pc98",
			"pc99",
			"sum",
			"min",
			"max",
			"avg",
			"median",
		)
	}
}

func normalizeLogsComputeType(raw, field string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "", "total", "timeseries":
		return value, nil
	default:
		return "", fmt.Errorf("%s must be %q or %q", field, "total", "timeseries")
	}
}

func normalizeLogsAggregateSortType(raw, field string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "alphabetical", "measure":
		return value, nil
	case "":
		return "", nil
	default:
		return "", fmt.Errorf("%s must be %q or %q", field, "alphabetical", "measure")
	}
}

func normalizeLogsSortOrder(raw, field string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "asc", "desc":
		return value, nil
	case "":
		return "", nil
	default:
		return "", fmt.Errorf("%s must be %q or %q", field, "asc", "desc")
	}
}

func stringArraySchema(description string) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": description,
		"items": map[string]any{
			"type": "string",
		},
	}
}

func arraySchema(description string, itemSchema map[string]any) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": description,
		"items":       itemSchema,
	}
}
