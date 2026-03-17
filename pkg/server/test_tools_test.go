package server

import (
	"context"
	"fmt"
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
	testToolDatadogReadID  = "datadog_read"

	testToolSlackUserTokenEnv = "SLACK_USER_TOKEN"
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
				"text": toolscommon.StringSchema("Message text."),
			},
			"text",
		),
		RequiredArguments: []string{"text"},
		RequiresNetwork:   true,
		Mutating:          true,
	}
}

func (p testSlackPostToolPlugin) NewHandler(config toolscore.ConfiguredTool) (toolscore.ToolHandler, error) {
	_ = config
	return toolscore.ToolHandlerFunc(p.handle), nil
}

func (p testSlackPostToolPlugin) handle(ctx context.Context, toolCtx toolscore.ToolContext, step *toolscore.Step, call *toolscore.ToolAction) error {
	_ = ctx
	_ = call
	if !p.host.NetworkEnabled(toolCtx) {
		toolCtx.SetPolicyError(step, fmt.Errorf("%s requires network.reachable_hosts to be configured", testToolSlackPostID))
		return nil
	}
	output, err := toolscommon.MarshalToolOutput(map[string]any{"ok": true})
	if err != nil {
		return err
	}
	toolCtx.SetOutput(step, output)
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
				"thread_ts": toolscommon.StringSchema("Thread timestamp."),
			},
			"thread_ts",
		),
		RequiredArguments: []string{"thread_ts"},
		RequiresNetwork:   true,
		ReadOnly:          true,
	}
}

func (p testSlackRepliesToolPlugin) NewHandler(config toolscore.ConfiguredTool) (toolscore.ToolHandler, error) {
	_ = config
	return toolscore.ToolHandlerFunc(p.handle), nil
}

func (p testSlackRepliesToolPlugin) handle(ctx context.Context, toolCtx toolscore.ToolContext, step *toolscore.Step, call *toolscore.ToolAction) error {
	_ = ctx
	_ = call
	if !p.host.NetworkEnabled(toolCtx) {
		toolCtx.SetPolicyError(step, fmt.Errorf("%s requires network.reachable_hosts to be configured", testToolSlackRepliesID))
		return nil
	}
	output, err := toolscommon.MarshalToolOutput(map[string]any{"ok": true})
	if err != nil {
		return err
	}
	toolCtx.SetOutput(step, output)
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
