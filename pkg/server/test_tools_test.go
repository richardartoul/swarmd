package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
)

const (
	testToolServerLogID    = "server_log"
	testToolSlackPostID    = "slack_post"
	testToolSlackRepliesID = "slack_replies"
	testToolSlackHistoryID = "slack_channel_history"
	testToolDatadogReadID  = "datadog_read"

	testToolSlackUserTokenEnv = "SLACK_USER_TOKEN"
	testToolSlackBaseURLEnv   = "SLACK_API_BASE_URL"
	testToolDatadogAPIKeyEnv  = "DD_API_KEY"
	testToolDatadogAppKeyEnv  = "DD_APP_KEY"
	testToolDatadogBaseURLEnv = "DD_BASE_URL"
)

var registerServerTestToolsOnce sync.Once

func init() {
	registerServerTestTools()
}

func registerServerTestTools() {
	registerServerTestToolsOnce.Do(func() {
		// Test builds register these implementations under the production tool IDs.
		// The server tests intentionally avoid importing the real tool packages,
		// so specs and runtime flows can exercise tool wiring without external clients.
		RegisterTool(func(host ToolHost) toolscore.ToolPlugin {
			return testServerLogToolPlugin{host: host}
		}, ToolRegistrationOptions{})
		RegisterTool(func(host ToolHost) toolscore.ToolPlugin {
			return testSlackPostToolPlugin{host: host}
		}, ToolRegistrationOptions{RequiredEnv: []string{testToolSlackUserTokenEnv}})
		RegisterTool(func(host ToolHost) toolscore.ToolPlugin {
			return testSlackRepliesToolPlugin{host: host}
		}, ToolRegistrationOptions{RequiredEnv: []string{testToolSlackUserTokenEnv}})
		RegisterTool(func(host ToolHost) toolscore.ToolPlugin {
			return testSlackHistoryToolPlugin{host: host}
		}, ToolRegistrationOptions{RequiredEnv: []string{testToolSlackUserTokenEnv}})
		RegisterTool(func(host ToolHost) toolscore.ToolPlugin {
			return testDatadogReadToolPlugin{host: host}
		}, ToolRegistrationOptions{RequiredEnv: []string{testToolDatadogAPIKeyEnv, testToolDatadogAppKeyEnv}})
	})
}

type testServerLogToolPlugin struct {
	host ToolHost
}

func (testServerLogToolPlugin) Definition() toolscore.ToolDefinition {
	return toolscore.ToolDefinition{
		Name:        testToolServerLogID,
		Description: "Write a message to the server logs with namespace and agent context.",
		Kind:        toolscore.ToolKindFunction,
		Parameters: toolscommon.ObjectSchema(
			map[string]any{
				"level":   toolscommon.StringSchema("Log level."),
				"message": toolscommon.StringSchema("Log message."),
			},
			"level",
			"message",
		),
		RequiredArguments: []string{"level", "message"},
		Mutating:          true,
	}
}

func (p testServerLogToolPlugin) NewHandler(config toolscore.ConfiguredTool) (toolscore.ToolHandler, error) {
	_ = config
	return toolscore.ToolHandlerFunc(p.handle), nil
}

func (p testServerLogToolPlugin) handle(ctx context.Context, toolCtx toolscore.ToolContext, step *toolscore.Step, call *toolscore.ToolAction) error {
	input, err := toolscore.DecodeToolInput[struct {
		Level   string `json:"level"`
		Message string `json:"message"`
	}](call.Input)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	runtime, err := p.host.Runtime(toolCtx)
	if err != nil {
		return err
	}
	triggerCtx := TriggerContext{
		NamespaceID: runtime.NamespaceID(),
		AgentID:     runtime.AgentID(),
	}
	if current, ok := p.host.TriggerContext(ctx); ok {
		triggerCtx = current
	}
	if logger := runtime.Logger(); logger != nil {
		logger.LogAgentCommand(triggerCtx, strings.TrimSpace(input.Level), strings.TrimSpace(input.Message))
	}
	output, err := toolscommon.MarshalToolOutput(map[string]any{"ok": true})
	if err != nil {
		return err
	}
	toolCtx.SetOutput(step, output)
	return nil
}

type testSlackToolConfig struct {
	DefaultChannel string `json:"default_channel"`
}

func decodeTestSlackToolConfig(toolID string, raw toolscore.ConfiguredTool) (testSlackToolConfig, error) {
	cfg, err := toolscommon.DecodeToolConfig[testSlackToolConfig](raw.Config)
	if err != nil {
		return testSlackToolConfig{}, fmt.Errorf("decode %s config: %w", toolID, err)
	}
	cfg.DefaultChannel = strings.TrimSpace(cfg.DefaultChannel)
	return cfg, nil
}

func testSlackRequestRuntime(host ToolHost, toolCtx toolscore.ToolContext, toolID string) (ToolRuntime, *http.Client, string, error) {
	runtime, err := host.Runtime(toolCtx)
	if err != nil {
		return nil, nil, "", err
	}
	baseURL := strings.TrimRight(runtime.LookupEnv(testToolSlackBaseURLEnv), "/")
	if baseURL == "" {
		return nil, nil, "", fmt.Errorf("%s requires %s", toolID, testToolSlackBaseURLEnv)
	}
	client := host.HTTPClient(toolCtx, toolscore.ToolHTTPClientOptions{
		ConnectTimeout:  10 * time.Second,
		FollowRedirects: true,
	})
	if client == nil {
		return nil, nil, "", fmt.Errorf("HTTP client factory is not configured")
	}
	return runtime, client, baseURL, nil
}

func performTestSlackRequest(ctx context.Context, client *http.Client, token, method, endpoint string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		encodedPayload, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshal Slack test request body: %w", err)
		}
		body = bytes.NewReader(encodedPayload)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	bodyBytes, _, err := toolscommon.ReadHTTPBodyLimited(resp.Body, toolscommon.DefaultHTTPResponseBytes)
	if err != nil {
		return nil, err
	}
	return bodyBytes, nil
}

type testSlackPostToolPlugin struct {
	host ToolHost
}

func (testSlackPostToolPlugin) Definition() toolscore.ToolDefinition {
	return toolscore.ToolDefinition{
		Name:        testToolSlackPostID,
		Description: "Post a Slack message or thread reply through the tool-owned Slack client.",
		Kind:        toolscore.ToolKindFunction,
		Parameters: toolscommon.ObjectSchema(
			map[string]any{
				"channel":   toolscommon.StringSchema("Slack conversation ID."),
				"text":      toolscommon.StringSchema("Message text."),
				"thread_ts": toolscommon.StringSchema("Optional Slack thread timestamp."),
			},
			"text",
		),
		RequiredArguments: []string{"text"},
		RequiresNetwork:   true,
		Mutating:          true,
	}
}

func (p testSlackPostToolPlugin) NewHandler(config toolscore.ConfiguredTool) (toolscore.ToolHandler, error) {
	cfg, err := decodeTestSlackToolConfig(testToolSlackPostID, config)
	if err != nil {
		return nil, err
	}
	return toolscore.ToolHandlerFunc(func(ctx context.Context, toolCtx toolscore.ToolContext, step *toolscore.Step, call *toolscore.ToolAction) error {
		return p.handle(ctx, toolCtx, step, call, cfg)
	}), nil
}

func (p testSlackPostToolPlugin) handle(ctx context.Context, toolCtx toolscore.ToolContext, step *toolscore.Step, call *toolscore.ToolAction, cfg testSlackToolConfig) error {
	if !p.host.NetworkEnabled(toolCtx) {
		toolCtx.SetPolicyError(step, fmt.Errorf("%s requires network.reachable_hosts to be configured", testToolSlackPostID))
		return nil
	}
	input, err := toolscore.DecodeToolInput[struct {
		Channel  string `json:"channel"`
		Text     string `json:"text"`
		ThreadTS string `json:"thread_ts"`
	}](call.Input)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	channel := toolscommon.FirstNonEmptyString(input.Channel, cfg.DefaultChannel)
	if channel == "" {
		toolCtx.SetPolicyError(step, fmt.Errorf(`channel must be provided via "channel" or tools[].config.default_channel`))
		return nil
	}
	text := strings.TrimSpace(input.Text)
	if text == "" {
		toolCtx.SetPolicyError(step, fmt.Errorf("text must not be empty"))
		return nil
	}
	runtime, client, baseURL, err := testSlackRequestRuntime(p.host, toolCtx, testToolSlackPostID)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	body, err := performTestSlackRequest(ctx, client, runtime.LookupEnv(testToolSlackUserTokenEnv), http.MethodPost, baseURL+"/chat.postMessage", map[string]any{
		"channel":   channel,
		"text":      text,
		"thread_ts": strings.TrimSpace(input.ThreadTS),
	})
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	toolCtx.SetOutput(step, string(body))
	return nil
}

type testSlackRepliesToolPlugin struct {
	host ToolHost
}

func (testSlackRepliesToolPlugin) Definition() toolscore.ToolDefinition {
	return toolscore.ToolDefinition{
		Name:        testToolSlackRepliesID,
		Description: "List Slack replies for one thread through the tool-owned Slack client.",
		Kind:        toolscore.ToolKindFunction,
		Parameters: toolscommon.ObjectSchema(
			map[string]any{
				"channel":   toolscommon.StringSchema("Slack conversation ID."),
				"thread_ts": toolscommon.StringSchema("Thread timestamp."),
				"after_ts":  toolscommon.StringSchema("Optional reply cursor."),
			},
			"thread_ts",
		),
		RequiredArguments: []string{"thread_ts"},
		RequiresNetwork:   true,
		ReadOnly:          true,
	}
}

func (p testSlackRepliesToolPlugin) NewHandler(config toolscore.ConfiguredTool) (toolscore.ToolHandler, error) {
	cfg, err := decodeTestSlackToolConfig(testToolSlackRepliesID, config)
	if err != nil {
		return nil, err
	}
	return toolscore.ToolHandlerFunc(func(ctx context.Context, toolCtx toolscore.ToolContext, step *toolscore.Step, call *toolscore.ToolAction) error {
		return p.handle(ctx, toolCtx, step, call, cfg)
	}), nil
}

func (p testSlackRepliesToolPlugin) handle(ctx context.Context, toolCtx toolscore.ToolContext, step *toolscore.Step, call *toolscore.ToolAction, cfg testSlackToolConfig) error {
	if !p.host.NetworkEnabled(toolCtx) {
		toolCtx.SetPolicyError(step, fmt.Errorf("%s requires network.reachable_hosts to be configured", testToolSlackRepliesID))
		return nil
	}
	input, err := toolscore.DecodeToolInput[struct {
		Channel  string `json:"channel"`
		ThreadTS string `json:"thread_ts"`
		AfterTS  string `json:"after_ts"`
	}](call.Input)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	channel := toolscommon.FirstNonEmptyString(input.Channel, cfg.DefaultChannel)
	if channel == "" {
		toolCtx.SetPolicyError(step, fmt.Errorf(`channel must be provided via "channel" or tools[].config.default_channel`))
		return nil
	}
	threadTS := strings.TrimSpace(input.ThreadTS)
	if threadTS == "" {
		toolCtx.SetPolicyError(step, fmt.Errorf("thread_ts must not be empty"))
		return nil
	}
	runtime, client, baseURL, err := testSlackRequestRuntime(p.host, toolCtx, testToolSlackRepliesID)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	target, err := url.Parse(baseURL + "/conversations.replies")
	if err != nil {
		return fmt.Errorf("parse Slack replies test URL: %w", err)
	}
	query := target.Query()
	query.Set("channel", channel)
	query.Set("ts", threadTS)
	if afterTS := strings.TrimSpace(input.AfterTS); afterTS != "" {
		query.Set("oldest", afterTS)
		query.Set("inclusive", "false")
	}
	target.RawQuery = query.Encode()
	body, err := performTestSlackRequest(ctx, client, runtime.LookupEnv(testToolSlackUserTokenEnv), http.MethodGet, target.String(), nil)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	toolCtx.SetOutput(step, string(body))
	return nil
}

type testSlackHistoryToolPlugin struct {
	host ToolHost
}

func (testSlackHistoryToolPlugin) Definition() toolscore.ToolDefinition {
	return toolscore.ToolDefinition{
		Name:        testToolSlackHistoryID,
		Description: "List Slack channel timeline messages newer than a given timestamp through the tool-owned Slack client.",
		Kind:        toolscore.ToolKindFunction,
		Parameters: toolscommon.ObjectSchema(
			map[string]any{
				"channel":      toolscommon.StringSchema("Slack conversation ID."),
				"after_ts":     toolscommon.StringSchema("Exclusive lower bound."),
				"before_ts":    toolscommon.StringSchema("Optional exclusive upper bound."),
				"max_messages": toolscommon.IntegerSchema("Optional maximum number of messages to return."),
			},
			"after_ts",
		),
		RequiredArguments: []string{"after_ts"},
		RequiresNetwork:   true,
		ReadOnly:          true,
	}
}

func (p testSlackHistoryToolPlugin) NewHandler(config toolscore.ConfiguredTool) (toolscore.ToolHandler, error) {
	cfg, err := decodeTestSlackToolConfig(testToolSlackHistoryID, config)
	if err != nil {
		return nil, err
	}
	return toolscore.ToolHandlerFunc(func(ctx context.Context, toolCtx toolscore.ToolContext, step *toolscore.Step, call *toolscore.ToolAction) error {
		return p.handle(ctx, toolCtx, step, call, cfg)
	}), nil
}

func (p testSlackHistoryToolPlugin) handle(ctx context.Context, toolCtx toolscore.ToolContext, step *toolscore.Step, call *toolscore.ToolAction, cfg testSlackToolConfig) error {
	if !p.host.NetworkEnabled(toolCtx) {
		toolCtx.SetPolicyError(step, fmt.Errorf("%s requires network.reachable_hosts to be configured", testToolSlackHistoryID))
		return nil
	}
	input, err := toolscore.DecodeToolInput[struct {
		Channel     string `json:"channel"`
		AfterTS     string `json:"after_ts"`
		BeforeTS    string `json:"before_ts"`
		MaxMessages int    `json:"max_messages"`
	}](call.Input)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	channel := toolscommon.FirstNonEmptyString(input.Channel, cfg.DefaultChannel)
	if channel == "" {
		toolCtx.SetPolicyError(step, fmt.Errorf(`channel must be provided via "channel" or tools[].config.default_channel`))
		return nil
	}
	afterTS := strings.TrimSpace(input.AfterTS)
	if afterTS == "" {
		toolCtx.SetPolicyError(step, fmt.Errorf("after_ts must not be empty"))
		return nil
	}
	runtime, client, baseURL, err := testSlackRequestRuntime(p.host, toolCtx, testToolSlackHistoryID)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	target, err := url.Parse(baseURL + "/conversations.history")
	if err != nil {
		return fmt.Errorf("parse Slack history test URL: %w", err)
	}
	query := target.Query()
	query.Set("channel", channel)
	query.Set("oldest", afterTS)
	query.Set("inclusive", "false")
	if beforeTS := strings.TrimSpace(input.BeforeTS); beforeTS != "" {
		query.Set("latest", beforeTS)
	}
	if input.MaxMessages > 0 {
		query.Set("limit", strconv.Itoa(input.MaxMessages))
	}
	target.RawQuery = query.Encode()
	body, err := performTestSlackRequest(ctx, client, runtime.LookupEnv(testToolSlackUserTokenEnv), http.MethodGet, target.String(), nil)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	toolCtx.SetOutput(step, string(body))
	return nil
}

type testDatadogReadToolPlugin struct {
	host ToolHost
}

func (testDatadogReadToolPlugin) Definition() toolscore.ToolDefinition {
	return toolscore.ToolDefinition{
		Name:        testToolDatadogReadID,
		Description: "Read incidents, monitors, dashboards, metrics, logs, and events from Datadog through the tool-owned Datadog client.",
		Kind:        toolscore.ToolKindFunction,
		Parameters: toolscommon.ObjectSchema(
			map[string]any{
				"action":    toolscommon.StringSchema("Read action."),
				"page_size": toolscommon.IntegerSchema("Page size."),
			},
			"action",
		),
		RequiredArguments: []string{"action"},
		RequiresNetwork:   true,
		ReadOnly:          true,
	}
}

func (p testDatadogReadToolPlugin) NewHandler(config toolscore.ConfiguredTool) (toolscore.ToolHandler, error) {
	_ = config
	return toolscore.ToolHandlerFunc(p.handle), nil
}

func (p testDatadogReadToolPlugin) handle(ctx context.Context, toolCtx toolscore.ToolContext, step *toolscore.Step, call *toolscore.ToolAction) error {
	if !p.host.NetworkEnabled(toolCtx) {
		toolCtx.SetPolicyError(step, fmt.Errorf("%s requires network.reachable_hosts to be configured", testToolDatadogReadID))
		return nil
	}
	input, err := toolscore.DecodeToolInput[struct {
		Action   string `json:"action"`
		PageSize int    `json:"page_size"`
	}](call.Input)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	if strings.TrimSpace(input.Action) != "list_incidents" {
		toolCtx.SetPolicyError(step, fmt.Errorf("unsupported action %q", strings.TrimSpace(input.Action)))
		return nil
	}
	runtime, err := p.host.Runtime(toolCtx)
	if err != nil {
		return err
	}
	baseURL := strings.TrimRight(runtime.LookupEnv(testToolDatadogBaseURLEnv), "/")
	if baseURL == "" {
		toolCtx.SetPolicyError(step, fmt.Errorf("%s requires %s", testToolDatadogReadID, testToolDatadogBaseURLEnv))
		return nil
	}
	client := p.host.HTTPClient(toolCtx, toolscore.ToolHTTPClientOptions{
		ConnectTimeout:  10 * time.Second,
		FollowRedirects: true,
	})
	if client == nil {
		return fmt.Errorf("HTTP client factory is not configured")
	}
	target, err := url.Parse(baseURL + "/api/v2/incidents")
	if err != nil {
		return fmt.Errorf("parse test Datadog URL: %w", err)
	}
	pageSize := input.PageSize
	if pageSize <= 0 {
		pageSize = 25
	}
	query := target.Query()
	query.Set("page[size]", strconv.Itoa(pageSize))
	query.Set("page[offset]", "0")
	target.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("DD-API-KEY", runtime.LookupEnv(testToolDatadogAPIKeyEnv))
	req.Header.Set("DD-APPLICATION-KEY", runtime.LookupEnv(testToolDatadogAppKeyEnv))
	resp, err := client.Do(req)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	defer resp.Body.Close()
	body, _, err := toolscommon.ReadHTTPBodyLimited(resp.Body, toolscommon.DefaultHTTPResponseBytes)
	if err != nil {
		return err
	}
	toolCtx.SetOutput(step, string(body))
	return nil
}
