package datadogread

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	datadogapi "github.com/DataDog/datadog-api-client-go/v2/api/datadog"
	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV1"
	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV2"
	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
)

const (
	datadogDefaultMaxRetries    = 3
	datadogMaxMetricsSeries     = 12
	datadogMaxMetricPoints      = 60
	datadogMaxLogMessageChars   = 500
	datadogMaxEventTextChars    = 500
	datadogMaxEventTitleChars   = 200
	datadogMaxMonitorMessageLen = 500
)

var datadogUnstableOperations = []string{
	"v2.GetIncident",
	"v2.ListIncidents",
}

type DatadogClientConfig struct {
	APIKey     string
	AppKey     string
	Site       string
	BaseURL    string
	HTTPClient *http.Client
}

type DatadogClient struct {
	apiClient *datadogapi.APIClient
	apiKey    string
	appKey    string
	site      string
	baseURL   string
}

func NewDatadogClientFromEnv(lookupEnv func(string) string) (*DatadogClient, error) {
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	return NewDatadogClient(DatadogClientConfig{
		APIKey:  lookupEnv(DatadogAPIKeyEnvVar),
		AppKey:  lookupEnv(DatadogAppKeyEnvVar),
		Site:    lookupEnv(DatadogSiteEnvVar),
		BaseURL: lookupEnv(datadogBaseURLEnvVar),
	})
}

func NewDatadogClient(cfg DatadogClientConfig) (*DatadogClient, error) {
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("datadog client requires %s", DatadogAPIKeyEnvVar)
	}
	appKey := strings.TrimSpace(cfg.AppKey)
	if appKey == "" {
		return nil, fmt.Errorf("datadog client requires %s", DatadogAppKeyEnvVar)
	}
	site := strings.TrimSpace(cfg.Site)
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")

	configuration := datadogapi.NewConfiguration()
	configuration.RetryConfiguration.EnableRetry = true
	configuration.RetryConfiguration.MaxRetries = datadogDefaultMaxRetries
	if cfg.HTTPClient != nil {
		configuration.HTTPClient = cfg.HTTPClient
	}
	if baseURL != "" {
		configuration.Servers = datadogapi.ServerConfigurations{{
			URL:         baseURL,
			Description: "custom Datadog server",
		}}
		configuration.OperationServers = map[string]datadogapi.ServerConfigurations{}
	}
	for _, operation := range datadogUnstableOperations {
		configuration.SetUnstableOperationEnabled(operation, true)
	}

	return &DatadogClient{
		apiClient: datadogapi.NewAPIClient(configuration),
		apiKey:    apiKey,
		appKey:    appKey,
		site:      site,
		baseURL:   baseURL,
	}, nil
}

func (c *DatadogClient) WithHTTPClient(httpClient *http.Client) (DatadogReadService, error) {
	if c == nil {
		return nil, fmt.Errorf("datadog client is nil")
	}
	if httpClient == nil {
		return c, nil
	}
	return NewDatadogClient(DatadogClientConfig{
		APIKey:     c.apiKey,
		AppKey:     c.appKey,
		Site:       c.site,
		BaseURL:    c.baseURL,
		HTTPClient: httpClient,
	})
}

func (c *DatadogClient) ExecuteRead(ctx context.Context, req DatadogReadRequest) (DatadogReadResult, error) {
	switch req.Action {
	case DatadogReadActionListIncidents:
		return c.listIncidents(ctx, req)
	case DatadogReadActionGetIncident:
		return c.getIncident(ctx, req)
	case DatadogReadActionListMonitors:
		return c.listMonitors(ctx, req)
	case DatadogReadActionGetMonitor:
		return c.getMonitor(ctx, req)
	case DatadogReadActionListDashboards:
		return c.listDashboards(ctx, req)
	case DatadogReadActionGetDashboard:
		return c.getDashboard(ctx, req)
	case DatadogReadActionQueryMetrics:
		return c.queryMetrics(ctx, req)
	case DatadogReadActionSearchLogs:
		return c.searchLogs(ctx, req)
	case DatadogReadActionAggregateLogs:
		return c.aggregateLogs(ctx, req)
	case DatadogReadActionListEvents:
		return c.listEvents(ctx, req)
	default:
		return DatadogReadResult{}, fmt.Errorf("unsupported Datadog action %q", req.Action)
	}
}

func (c *DatadogClient) requestContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithValue(ctx, datadogapi.ContextAPIKeys, map[string]datadogapi.APIKey{
		"apiKeyAuth": {Key: c.apiKey},
		"appKeyAuth": {Key: c.appKey},
	})
	if c.site != "" {
		ctx = context.WithValue(ctx, datadogapi.ContextServerVariables, map[string]string{
			"site": c.site,
		})
	}
	return ctx
}

func (c *DatadogClient) listIncidents(ctx context.Context, req DatadogReadRequest) (DatadogReadResult, error) {
	params := datadogV2.NewListIncidentsOptionalParameters().
		WithPageSize(int64(req.PageSize)).
		WithPageOffset(req.PageOffset)
	response, _, err := datadogV2.NewIncidentsApi(c.apiClient).ListIncidents(c.requestContext(ctx), *params)
	if err != nil {
		return DatadogReadResult{}, fmt.Errorf("list incidents: %w", err)
	}
	items := make([]DatadogIncident, 0, len(response.Data))
	for _, incident := range response.Data {
		items = append(items, normalizeDatadogIncident(incident))
	}
	result := DatadogReadResult{
		Action:     req.Action,
		Items:      items,
		PageSize:   req.PageSize,
		PageOffset: req.PageOffset,
	}
	if response.Meta != nil && response.Meta.Pagination != nil && response.Meta.Pagination.NextOffset != nil {
		nextOffset := *response.Meta.Pagination.NextOffset
		result.NextOffset = &nextOffset
	}
	return result, nil
}

func (c *DatadogClient) getIncident(ctx context.Context, req DatadogReadRequest) (DatadogReadResult, error) {
	response, _, err := datadogV2.NewIncidentsApi(c.apiClient).GetIncident(c.requestContext(ctx), req.IncidentID)
	if err != nil {
		return DatadogReadResult{}, fmt.Errorf("get incident %q: %w", req.IncidentID, err)
	}
	return DatadogReadResult{
		Action: req.Action,
		Item:   normalizeDatadogIncident(response.Data),
	}, nil
}

func (c *DatadogClient) listMonitors(ctx context.Context, req DatadogReadRequest) (DatadogReadResult, error) {
	params := datadogV1.NewListMonitorsOptionalParameters().
		WithPage(req.Page).
		WithPageSize(int32(req.PageSize))
	if strings.TrimSpace(req.Query) != "" {
		params.WithName(strings.TrimSpace(req.Query))
	}
	response, _, err := datadogV1.NewMonitorsApi(c.apiClient).ListMonitors(c.requestContext(ctx), *params)
	if err != nil {
		return DatadogReadResult{}, fmt.Errorf("list monitors: %w", err)
	}
	items := make([]DatadogMonitor, 0, len(response))
	for _, monitor := range response {
		items = append(items, normalizeDatadogMonitor(monitor))
	}
	return DatadogReadResult{
		Action:   req.Action,
		Items:    items,
		Query:    strings.TrimSpace(req.Query),
		Page:     req.Page,
		PageSize: req.PageSize,
	}, nil
}

func (c *DatadogClient) getMonitor(ctx context.Context, req DatadogReadRequest) (DatadogReadResult, error) {
	response, _, err := datadogV1.NewMonitorsApi(c.apiClient).GetMonitor(c.requestContext(ctx), req.MonitorID)
	if err != nil {
		return DatadogReadResult{}, fmt.Errorf("get monitor %d: %w", req.MonitorID, err)
	}
	return DatadogReadResult{
		Action: req.Action,
		Item:   normalizeDatadogMonitor(response),
	}, nil
}

func (c *DatadogClient) listDashboards(ctx context.Context, req DatadogReadRequest) (DatadogReadResult, error) {
	params := datadogV1.NewListDashboardsOptionalParameters().
		WithCount(int64(req.PageSize)).
		WithStart(req.PageOffset)
	response, _, err := datadogV1.NewDashboardsApi(c.apiClient).ListDashboards(c.requestContext(ctx), *params)
	if err != nil {
		return DatadogReadResult{}, fmt.Errorf("list dashboards: %w", err)
	}
	items := make([]DatadogDashboard, 0, len(response.Dashboards))
	for _, dashboard := range response.Dashboards {
		items = append(items, normalizeDatadogDashboardSummary(dashboard))
	}
	return DatadogReadResult{
		Action:     req.Action,
		Items:      items,
		PageSize:   req.PageSize,
		PageOffset: req.PageOffset,
	}, nil
}

func (c *DatadogClient) getDashboard(ctx context.Context, req DatadogReadRequest) (DatadogReadResult, error) {
	response, _, err := datadogV1.NewDashboardsApi(c.apiClient).GetDashboard(c.requestContext(ctx), req.DashboardID)
	if err != nil {
		return DatadogReadResult{}, fmt.Errorf("get dashboard %q: %w", req.DashboardID, err)
	}
	item, err := rawJSONValue(response)
	if err != nil {
		return DatadogReadResult{}, fmt.Errorf("encode dashboard %q: %w", req.DashboardID, err)
	}
	return DatadogReadResult{
		Action: req.Action,
		Item:   item,
	}, nil
}

func rawJSONValue(value any) (any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func (c *DatadogClient) queryMetrics(ctx context.Context, req DatadogReadRequest) (DatadogReadResult, error) {
	response, _, err := datadogV1.NewMetricsApi(c.apiClient).QueryMetrics(c.requestContext(ctx), req.From.Unix(), req.To.Unix(), req.Query)
	if err != nil {
		return DatadogReadResult{}, fmt.Errorf("query metrics: %w", err)
	}
	series := make([]DatadogMetricSeries, 0, toolscommon.MinInt(len(response.Series), datadogMaxMetricsSeries))
	for index, metadata := range response.Series {
		if index >= datadogMaxMetricsSeries {
			break
		}
		series = append(series, normalizeDatadogMetricSeries(metadata))
	}
	result := DatadogReadResult{
		Action: req.Action,
		Query:  req.Query,
		From:   formatRFC3339FromUnixMillis(response.FromDate),
		To:     formatRFC3339FromUnixMillis(response.ToDate),
		Status: strings.TrimSpace(response.GetStatus()),
		Series: series,
	}
	if len(response.Series) > datadogMaxMetricsSeries {
		result.SeriesTruncated = true
	}
	if result.From == "" {
		result.From = formatRFC3339(req.From)
	}
	if result.To == "" {
		result.To = formatRFC3339(req.To)
	}
	return result, nil
}

func (c *DatadogClient) searchLogs(ctx context.Context, req DatadogReadRequest) (DatadogReadResult, error) {
	params := datadogV2.NewListLogsGetOptionalParameters().
		WithFilterQuery(req.Query).
		WithFilterFrom(req.From).
		WithFilterTo(req.To).
		WithPageLimit(int32(req.PageSize))
	if strings.TrimSpace(req.StorageTier) != "" {
		storageTier, err := datadogV2.NewLogsStorageTierFromValue(strings.TrimSpace(req.StorageTier))
		if err != nil {
			return DatadogReadResult{}, fmt.Errorf("search logs: %w", err)
		}
		params.WithFilterStorageTier(*storageTier)
	}
	if strings.TrimSpace(req.PageCursor) != "" {
		params.WithPageCursor(strings.TrimSpace(req.PageCursor))
	}
	response, _, err := datadogV2.NewLogsApi(c.apiClient).ListLogsGet(c.requestContext(ctx), *params)
	if err != nil {
		return DatadogReadResult{}, fmt.Errorf("search logs: %w", err)
	}
	items := make([]DatadogLogEntry, 0, len(response.Data))
	for _, entry := range response.Data {
		items = append(items, normalizeDatadogLog(entry))
	}
	result := DatadogReadResult{
		Action:   req.Action,
		Items:    items,
		Query:    req.Query,
		From:     formatRFC3339(req.From),
		To:       formatRFC3339(req.To),
		PageSize: req.PageSize,
	}
	if response.Meta != nil {
		result.ElapsedMS = response.Meta.GetElapsed()
		if response.Meta.Page != nil && response.Meta.Page.After != nil {
			result.NextCursor = strings.TrimSpace(*response.Meta.Page.After)
		}
		if response.Meta.Status != nil {
			result.Status = string(*response.Meta.Status)
		}
		result.Warnings = normalizeDatadogWarnings(response.Meta.Warnings)
	}
	return result, nil
}

func (c *DatadogClient) aggregateLogs(ctx context.Context, req DatadogReadRequest) (DatadogReadResult, error) {
	filter := datadogV2.LogsQueryFilter{}
	query := req.Query
	from := formatRFC3339(req.From)
	to := formatRFC3339(req.To)
	filter.Query = &query
	filter.From = &from
	filter.To = &to
	if len(req.Indexes) > 0 {
		filter.Indexes = append([]string(nil), req.Indexes...)
	}
	if strings.TrimSpace(req.StorageTier) != "" {
		storageTier, err := datadogV2.NewLogsStorageTierFromValue(strings.TrimSpace(req.StorageTier))
		if err != nil {
			return DatadogReadResult{}, fmt.Errorf("aggregate logs: %w", err)
		}
		filter.StorageTier = storageTier
	}
	compute, err := buildDatadogLogsAggregateCompute(req.Compute)
	if err != nil {
		return DatadogReadResult{}, fmt.Errorf("aggregate logs: %w", err)
	}
	groupBy, err := buildDatadogLogsAggregateGroupBy(req.GroupBy)
	if err != nil {
		return DatadogReadResult{}, fmt.Errorf("aggregate logs: %w", err)
	}
	body := datadogV2.LogsAggregateRequest{
		Filter:  &filter,
		Compute: compute,
	}
	if len(groupBy) > 0 {
		body.GroupBy = groupBy
	}
	if strings.TrimSpace(req.PageCursor) != "" {
		cursor := strings.TrimSpace(req.PageCursor)
		body.Page = &datadogV2.LogsAggregateRequestPage{
			Cursor: &cursor,
		}
	}
	response, _, err := datadogV2.NewLogsApi(c.apiClient).AggregateLogs(c.requestContext(ctx), body)
	if err != nil {
		return DatadogReadResult{}, fmt.Errorf("aggregate logs: %w", err)
	}
	items := make([]DatadogLogsAggregateBucket, 0)
	if response.Data != nil {
		items = make([]DatadogLogsAggregateBucket, 0, len(response.Data.GetBuckets()))
		for _, bucket := range response.Data.GetBuckets() {
			items = append(items, normalizeDatadogLogsAggregateBucket(bucket))
		}
	}
	result := DatadogReadResult{
		Action:   req.Action,
		Items:    items,
		Indexes:  append([]string(nil), req.Indexes...),
		Computes: withDatadogLogsAggregateComputeIDs(req.Compute),
		GroupBy:  cloneDatadogLogsAggregateGroupBy(req.GroupBy),
		Query:    req.Query,
		From:     formatRFC3339(req.From),
		To:       formatRFC3339(req.To),
	}
	if response.Meta != nil {
		result.ElapsedMS = response.Meta.GetElapsed()
		if response.Meta.Page != nil && response.Meta.Page.After != nil {
			result.NextCursor = strings.TrimSpace(*response.Meta.Page.After)
		}
		if response.Meta.Status != nil {
			result.Status = string(*response.Meta.Status)
		}
		result.Warnings = normalizeDatadogWarnings(response.Meta.Warnings)
	}
	return result, nil
}

func (c *DatadogClient) listEvents(ctx context.Context, req DatadogReadRequest) (DatadogReadResult, error) {
	params := datadogV1.NewListEventsOptionalParameters()
	if req.Page > 0 {
		params.WithPage(int32(req.Page))
	}
	response, _, err := datadogV1.NewEventsApi(c.apiClient).ListEvents(c.requestContext(ctx), req.From.Unix(), req.To.Unix(), *params)
	if err != nil {
		return DatadogReadResult{}, fmt.Errorf("list events: %w", err)
	}
	items := make([]DatadogEvent, 0, len(response.Events))
	for _, event := range response.Events {
		items = append(items, normalizeDatadogEvent(event))
	}
	return DatadogReadResult{
		Action: req.Action,
		Items:  items,
		From:   formatRFC3339(req.From),
		To:     formatRFC3339(req.To),
		Page:   req.Page,
		Status: strings.TrimSpace(response.GetStatus()),
	}, nil
}

func normalizeDatadogIncident(data datadogV2.IncidentResponseData) DatadogIncident {
	result := DatadogIncident{
		ID: strings.TrimSpace(data.Id),
	}
	if data.Attributes == nil {
		return result
	}
	attributes := data.Attributes
	result.Title = strings.TrimSpace(attributes.Title)
	if attributes.PublicId != nil {
		result.PublicID = *attributes.PublicId
	}
	if attributes.Severity != nil {
		result.Severity = string(*attributes.Severity)
	}
	if state, ok := attributes.GetStateOk(); ok && state != nil {
		result.State = strings.TrimSpace(*state)
	}
	if attributes.Created != nil {
		result.CreatedAt = formatRFC3339(*attributes.Created)
	}
	if attributes.Modified != nil {
		result.ModifiedAt = formatRFC3339(*attributes.Modified)
	}
	if attributes.Declared != nil {
		result.DeclaredAt = formatRFC3339(*attributes.Declared)
	}
	if detected, ok := attributes.GetDetectedOk(); ok && detected != nil {
		result.DetectedAt = formatRFC3339(*detected)
	}
	if resolved, ok := attributes.GetResolvedOk(); ok && resolved != nil {
		result.ResolvedAt = formatRFC3339(*resolved)
	}
	if attributes.CustomerImpacted != nil {
		value := *attributes.CustomerImpacted
		result.CustomerImpacted = &value
	}
	if scope, ok := attributes.GetCustomerImpactScopeOk(); ok && scope != nil {
		result.CustomerImpactScope = strings.TrimSpace(*scope)
	}
	if attributes.IncidentTypeUuid != nil {
		result.IncidentTypeUUID = strings.TrimSpace(*attributes.IncidentTypeUuid)
	}
	if attributes.IsTest != nil {
		value := *attributes.IsTest
		result.IsTest = &value
	}
	return result
}

func normalizeDatadogMonitor(monitor datadogV1.Monitor) DatadogMonitor {
	result := DatadogMonitor{
		Query: strings.TrimSpace(monitor.Query),
		Type:  string(monitor.Type),
		Tags:  append([]string(nil), monitor.Tags...),
	}
	if monitor.Id != nil {
		result.ID = *monitor.Id
	}
	if monitor.Name != nil {
		result.Name = strings.TrimSpace(*monitor.Name)
	}
	if monitor.OverallState != nil {
		result.OverallState = string(*monitor.OverallState)
	}
	if monitor.Message != nil {
		result.Message = truncateString(strings.TrimSpace(*monitor.Message), datadogMaxMonitorMessageLen)
	}
	if priority := monitor.Priority.Get(); priority != nil {
		value := *priority
		result.Priority = &value
	}
	if monitor.Created != nil {
		result.CreatedAt = formatRFC3339(*monitor.Created)
	}
	if monitor.Modified != nil {
		result.ModifiedAt = formatRFC3339(*monitor.Modified)
	}
	if monitor.Multi != nil {
		value := *monitor.Multi
		result.Multi = &value
	}
	return result
}

func normalizeDatadogDashboardSummary(dashboard datadogV1.DashboardSummaryDefinition) DatadogDashboard {
	result := DatadogDashboard{
		Title: strings.TrimSpace(dashboard.GetTitle()),
	}
	if dashboard.Id != nil {
		result.ID = strings.TrimSpace(*dashboard.Id)
	}
	if dashboard.LayoutType != nil {
		result.LayoutType = string(*dashboard.LayoutType)
	}
	if description := dashboard.Description.Get(); description != nil {
		result.Description = strings.TrimSpace(*description)
	}
	if dashboard.AuthorHandle != nil {
		result.AuthorHandle = strings.TrimSpace(*dashboard.AuthorHandle)
	}
	if dashboard.Url != nil {
		result.URL = strings.TrimSpace(*dashboard.Url)
	}
	if dashboard.CreatedAt != nil {
		result.CreatedAt = formatRFC3339(*dashboard.CreatedAt)
	}
	if dashboard.ModifiedAt != nil {
		result.ModifiedAt = formatRFC3339(*dashboard.ModifiedAt)
	}
	if dashboard.IsReadOnly != nil {
		value := *dashboard.IsReadOnly
		result.IsReadOnly = &value
	}
	return result
}

func normalizeDatadogMetricSeries(metadata datadogV1.MetricsQueryMetadata) DatadogMetricSeries {
	result := DatadogMetricSeries{
		Metric:      strings.TrimSpace(metadata.GetMetric()),
		DisplayName: strings.TrimSpace(metadata.GetDisplayName()),
		Expression:  strings.TrimSpace(metadata.GetExpression()),
		Scope:       strings.TrimSpace(metadata.GetScope()),
		Tags:        append([]string(nil), metadata.TagSet...),
	}
	if aggr := metadata.Aggr.Get(); aggr != nil {
		result.Aggregation = strings.TrimSpace(*aggr)
	}
	if metadata.QueryIndex != nil {
		result.QueryIndex = *metadata.QueryIndex
	}
	if metadata.Interval != nil {
		result.IntervalMS = *metadata.Interval
	}
	if metadata.Length != nil {
		result.Length = *metadata.Length
	}
	if metadata.Start != nil {
		result.StartMS = *metadata.Start
	}
	if metadata.End != nil {
		result.EndMS = *metadata.End
	}
	points := make([]DatadogMetricPoint, 0, toolscommon.MinInt(len(metadata.Pointlist), datadogMaxMetricPoints))
	for index, point := range metadata.Pointlist {
		if index >= datadogMaxMetricPoints {
			result.PointsTruncated = true
			break
		}
		if len(point) < 2 || point[0] == nil || point[1] == nil {
			continue
		}
		points = append(points, DatadogMetricPoint{
			TimestampMS: int64(*point[0]),
			Value:       *point[1],
		})
	}
	result.Points = points
	return result
}

func normalizeDatadogLog(log datadogV2.Log) DatadogLogEntry {
	result := DatadogLogEntry{
		ID: strings.TrimSpace(log.GetId()),
	}
	if log.Attributes == nil {
		return result
	}
	attributes := log.Attributes
	if attributes.Timestamp != nil {
		result.Timestamp = formatRFC3339(*attributes.Timestamp)
	}
	if attributes.Service != nil {
		result.Service = strings.TrimSpace(*attributes.Service)
	}
	if attributes.Status != nil {
		result.Status = strings.TrimSpace(*attributes.Status)
	}
	if attributes.Host != nil {
		result.Host = strings.TrimSpace(*attributes.Host)
	}
	result.Tags = append([]string(nil), attributes.Tags...)
	if attributes.Message != nil {
		result.Message = truncateString(strings.TrimSpace(*attributes.Message), datadogMaxLogMessageChars)
	}
	return result
}

func normalizeDatadogLogsAggregateBucket(bucket datadogV2.LogsAggregateBucket) DatadogLogsAggregateBucket {
	result := DatadogLogsAggregateBucket{}
	if len(bucket.By) > 0 {
		result.By = make(map[string]any, len(bucket.By))
		for key, value := range bucket.By {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			if text, ok := value.(string); ok {
				result.By[key] = strings.TrimSpace(text)
				continue
			}
			result.By[key] = value
		}
	}
	if len(bucket.Computes) > 0 {
		result.Computes = make(map[string]any, len(bucket.Computes))
		for key, value := range bucket.Computes {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			normalized := normalizeDatadogLogsAggregateBucketValue(value)
			if normalized == nil {
				continue
			}
			result.Computes[key] = normalized
		}
		if len(result.Computes) == 0 {
			result.Computes = nil
		}
	}
	if len(result.By) == 0 {
		result.By = nil
	}
	return result
}

func normalizeDatadogLogsAggregateBucketValue(value datadogV2.LogsAggregateBucketValue) any {
	if single := value.LogsAggregateBucketValueSingleString; single != nil {
		return strings.TrimSpace(*single)
	}
	if single := value.LogsAggregateBucketValueSingleNumber; single != nil {
		return *single
	}
	if series := value.LogsAggregateBucketValueTimeseries; series != nil {
		points := make([]DatadogLogsAggregatePoint, 0, toolscommon.MinInt(len(series.Items), datadogMaxMetricPoints))
		var truncated bool
		for idx, point := range series.Items {
			if idx >= datadogMaxMetricPoints {
				truncated = true
				break
			}
			normalized := DatadogLogsAggregatePoint{}
			if point.Time != nil {
				normalized.Time = strings.TrimSpace(*point.Time)
			}
			if point.Value != nil {
				normalized.Value = *point.Value
			}
			points = append(points, normalized)
		}
		return DatadogLogsAggregateTimeseries{
			Points:    points,
			Truncated: truncated,
		}
	}
	if value.UnparsedObject != nil {
		return value.UnparsedObject
	}
	return nil
}

func normalizeDatadogWarnings(warnings []datadogV2.LogsWarning) []string {
	if len(warnings) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		parts := make([]string, 0, 3)
		if code := strings.TrimSpace(warning.GetCode()); code != "" {
			parts = append(parts, code)
		}
		if title := strings.TrimSpace(warning.GetTitle()); title != "" {
			parts = append(parts, title)
		}
		if detail := strings.TrimSpace(warning.GetDetail()); detail != "" {
			parts = append(parts, detail)
		}
		if len(parts) == 0 {
			continue
		}
		normalized = append(normalized, strings.Join(parts, ": "))
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func normalizeDatadogEvent(event datadogV1.Event) DatadogEvent {
	result := DatadogEvent{
		ID:   toolscommon.FirstNonEmptyString(event.GetIdStr(), formatInt64Ptr(event.Id)),
		Tags: append([]string(nil), event.Tags...),
	}
	if event.Title != nil {
		result.Title = truncateString(strings.TrimSpace(*event.Title), datadogMaxEventTitleChars)
	}
	if event.Text != nil {
		result.Text = truncateString(strings.TrimSpace(*event.Text), datadogMaxEventTextChars)
	}
	if event.AlertType != nil {
		result.AlertType = string(*event.AlertType)
	}
	if priority, ok := event.GetPriorityOk(); ok && priority != nil {
		result.Priority = string(*priority)
	}
	if event.SourceTypeName != nil {
		result.SourceTypeName = strings.TrimSpace(*event.SourceTypeName)
	}
	if event.Host != nil {
		result.Host = strings.TrimSpace(*event.Host)
	}
	if event.Url != nil {
		result.URL = strings.TrimSpace(*event.Url)
	}
	if event.DateHappened != nil {
		result.DateHappened = formatRFC3339(time.Unix(*event.DateHappened, 0))
	}
	return result
}

func formatRFC3339(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func formatRFC3339FromUnixMillis(value *int64) string {
	if value == nil || *value == 0 {
		return ""
	}
	return formatRFC3339(time.UnixMilli(*value))
}

func formatInt64Ptr(value *int64) string {
	if value == nil {
		return ""
	}
	return fmt.Sprintf("%d", *value)
}

func buildDatadogLogsAggregateCompute(values []DatadogLogsAggregateCompute) ([]datadogV2.LogsCompute, error) {
	if len(values) == 0 {
		return nil, nil
	}
	compute := make([]datadogV2.LogsCompute, 0, len(values))
	for _, value := range values {
		aggregation, err := datadogV2.NewLogsAggregationFunctionFromValue(strings.TrimSpace(value.Aggregation))
		if err != nil {
			return nil, err
		}
		rule := datadogV2.LogsCompute{
			Aggregation: *aggregation,
		}
		if strings.TrimSpace(value.Interval) != "" {
			interval := strings.TrimSpace(value.Interval)
			rule.Interval = &interval
		}
		if strings.TrimSpace(value.Metric) != "" {
			metric := strings.TrimSpace(value.Metric)
			rule.Metric = &metric
		}
		if strings.TrimSpace(value.Type) != "" {
			computeType, err := datadogV2.NewLogsComputeTypeFromValue(strings.TrimSpace(value.Type))
			if err != nil {
				return nil, err
			}
			rule.Type = computeType
		}
		compute = append(compute, rule)
	}
	return compute, nil
}

func buildDatadogLogsAggregateGroupBy(values []DatadogLogsAggregateGroupBy) ([]datadogV2.LogsGroupBy, error) {
	if len(values) == 0 {
		return nil, nil
	}
	groupBy := make([]datadogV2.LogsGroupBy, 0, len(values))
	for _, value := range values {
		rule := datadogV2.LogsGroupBy{
			Facet: strings.TrimSpace(value.Facet),
		}
		if value.Limit > 0 {
			limit := value.Limit
			rule.Limit = &limit
		}
		if value.Sort != nil {
			sortRule := datadogV2.LogsAggregateSort{}
			if strings.TrimSpace(value.Sort.Order) != "" {
				order, err := datadogV2.NewLogsSortOrderFromValue(strings.TrimSpace(value.Sort.Order))
				if err != nil {
					return nil, err
				}
				sortRule.Order = order
			}
			if strings.TrimSpace(value.Sort.Type) != "" {
				sortType, err := datadogV2.NewLogsAggregateSortTypeFromValue(strings.TrimSpace(value.Sort.Type))
				if err != nil {
					return nil, err
				}
				sortRule.Type = sortType
			}
			if strings.TrimSpace(value.Sort.Aggregation) != "" {
				aggregation, err := datadogV2.NewLogsAggregationFunctionFromValue(strings.TrimSpace(value.Sort.Aggregation))
				if err != nil {
					return nil, err
				}
				sortRule.Aggregation = aggregation
			}
			if strings.TrimSpace(value.Sort.Metric) != "" {
				metric := strings.TrimSpace(value.Sort.Metric)
				sortRule.Metric = &metric
			}
			rule.Sort = &sortRule
		}
		groupBy = append(groupBy, rule)
	}
	return groupBy, nil
}

func withDatadogLogsAggregateComputeIDs(values []DatadogLogsAggregateCompute) []DatadogLogsAggregateCompute {
	if len(values) == 0 {
		return nil
	}
	compute := make([]DatadogLogsAggregateCompute, 0, len(values))
	for idx, value := range values {
		cloned := value
		cloned.ID = fmt.Sprintf("c%d", idx)
		compute = append(compute, cloned)
	}
	return compute
}

func cloneDatadogLogsAggregateGroupBy(values []DatadogLogsAggregateGroupBy) []DatadogLogsAggregateGroupBy {
	if len(values) == 0 {
		return nil
	}
	groupBy := make([]DatadogLogsAggregateGroupBy, 0, len(values))
	for _, value := range values {
		cloned := value
		if value.Sort != nil {
			sortRule := *value.Sort
			cloned.Sort = &sortRule
		}
		groupBy = append(groupBy, cloned)
	}
	return groupBy
}

func truncateString(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}
