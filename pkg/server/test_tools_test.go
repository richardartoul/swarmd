package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
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
	testToolSlackDMID      = "slack_dm"
	testToolSlackRepliesID = "slack_replies"
	testToolSlackHistoryID = "slack_channel_history"
	testToolDatadogReadID  = "datadog_read"
	testToolGitHubRepoID   = "github_read_repo"
	testToolGitHubReviewID = "github_read_reviews"
	testToolGitHubCIID     = "github_read_ci"

	testToolSlackUserTokenEnv = "SLACK_USER_TOKEN"
	testToolSlackBaseURLEnv   = "SLACK_API_BASE_URL"
	testToolDatadogAPIKeyEnv  = "DD_API_KEY"
	testToolDatadogAppKeyEnv  = "DD_APP_KEY"
	testToolDatadogBaseURLEnv = "DD_BASE_URL"
	testToolGitHubTokenEnv    = "GITHUB_TOKEN"
	testToolGitHubBaseURLEnv  = "GITHUB_API_BASE_URL"
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
			return testSlackDMToolPlugin{host: host}
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
		RegisterTool(func(host ToolHost) toolscore.ToolPlugin {
			return testGitHubRepoToolPlugin{host: host}
		}, ToolRegistrationOptions{RequiredEnv: []string{testToolGitHubTokenEnv}})
		RegisterTool(func(host ToolHost) toolscore.ToolPlugin {
			return testGitHubReviewsToolPlugin{host: host}
		}, ToolRegistrationOptions{RequiredEnv: []string{testToolGitHubTokenEnv}})
		RegisterTool(func(host ToolHost) toolscore.ToolPlugin {
			return testGitHubCIToolPlugin{host: host}
		}, ToolRegistrationOptions{RequiredEnv: []string{testToolGitHubTokenEnv}})
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

type testSlackDMToolPlugin struct {
	host ToolHost
}

func (testSlackDMToolPlugin) Definition() toolscore.ToolDefinition {
	parameters := toolscommon.ObjectSchema(
		map[string]any{
			"user_id": toolscommon.StringSchema("Slack user ID."),
			"email":   toolscommon.StringSchema("Slack user email."),
			"text":    toolscommon.StringSchema("Message text."),
		},
		"text",
	)
	parameters["oneOf"] = []map[string]any{
		{"required": []string{"user_id"}},
		{"required": []string{"email"}},
	}
	return toolscore.ToolDefinition{
		Name:              testToolSlackDMID,
		Description:       "Send a Slack direct message to one user through the tool-owned Slack client.",
		Kind:              toolscore.ToolKindFunction,
		Parameters:        parameters,
		RequiredArguments: []string{"text"},
		RequiresNetwork:   true,
		Mutating:          true,
	}
}

func (p testSlackDMToolPlugin) NewHandler(config toolscore.ConfiguredTool) (toolscore.ToolHandler, error) {
	if err := toolscommon.ValidateNoToolConfig(testToolSlackDMID, config.Config); err != nil {
		return nil, err
	}
	return toolscore.ToolHandlerFunc(p.handle), nil
}

func (p testSlackDMToolPlugin) handle(ctx context.Context, toolCtx toolscore.ToolContext, step *toolscore.Step, call *toolscore.ToolAction) error {
	if !p.host.NetworkEnabled(toolCtx) {
		toolCtx.SetPolicyError(step, fmt.Errorf("%s requires network.reachable_hosts to be configured", testToolSlackDMID))
		return nil
	}
	input, err := toolscore.DecodeToolInput[struct {
		UserID string `json:"user_id"`
		Email  string `json:"email"`
		Text   string `json:"text"`
	}](call.Input)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	text := strings.TrimSpace(input.Text)
	if text == "" {
		toolCtx.SetPolicyError(step, fmt.Errorf("text must not be empty"))
		return nil
	}
	userID := strings.TrimSpace(input.UserID)
	email := strings.ToLower(strings.TrimSpace(input.Email))
	switch {
	case userID == "" && email == "":
		toolCtx.SetPolicyError(step, fmt.Errorf("exactly one of user_id or email must be provided"))
		return nil
	case userID != "" && email != "":
		toolCtx.SetPolicyError(step, fmt.Errorf("exactly one of user_id or email must be provided"))
		return nil
	}
	runtime, client, baseURL, err := testSlackRequestRuntime(p.host, toolCtx, testToolSlackDMID)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	if email != "" {
		target, err := url.Parse(baseURL + "/users.lookupByEmail")
		if err != nil {
			return fmt.Errorf("parse Slack dm lookup test URL: %w", err)
		}
		query := target.Query()
		query.Set("email", email)
		target.RawQuery = query.Encode()
		body, err := performTestSlackRequest(ctx, client, runtime.LookupEnv(testToolSlackUserTokenEnv), http.MethodGet, target.String(), nil)
		if err != nil {
			toolCtx.SetPolicyError(step, err)
			return nil
		}
		var response struct {
			User struct {
				ID string `json:"id"`
			} `json:"user"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			return fmt.Errorf("decode slack dm lookup response: %w", err)
		}
		userID = strings.TrimSpace(response.User.ID)
		if userID == "" {
			toolCtx.SetPolicyError(step, fmt.Errorf("slack dm lookup response missing user.id"))
			return nil
		}
	}
	openBody, err := performTestSlackRequest(ctx, client, runtime.LookupEnv(testToolSlackUserTokenEnv), http.MethodPost, baseURL+"/conversations.open", map[string]any{
		"users": userID,
	})
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	var openResponse struct {
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	if err := json.Unmarshal(openBody, &openResponse); err != nil {
		return fmt.Errorf("decode slack dm open response: %w", err)
	}
	channel := strings.TrimSpace(openResponse.Channel.ID)
	if channel == "" {
		toolCtx.SetPolicyError(step, fmt.Errorf("slack dm open response missing channel.id"))
		return nil
	}
	postBody, err := performTestSlackRequest(ctx, client, runtime.LookupEnv(testToolSlackUserTokenEnv), http.MethodPost, baseURL+"/chat.postMessage", map[string]any{
		"channel": channel,
		"text":    text,
	})
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	toolCtx.SetOutput(step, string(postBody))
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

func testGitHubRequestRuntime(host ToolHost, toolCtx toolscore.ToolContext, toolID string) (ToolRuntime, *http.Client, string, error) {
	runtime, err := host.Runtime(toolCtx)
	if err != nil {
		return nil, nil, "", err
	}
	baseURL := strings.TrimRight(runtime.LookupEnv(testToolGitHubBaseURLEnv), "/")
	if baseURL == "" {
		return nil, nil, "", fmt.Errorf("%s requires %s", toolID, testToolGitHubBaseURLEnv)
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

func performTestGitHubRequest(ctx context.Context, client *http.Client, token, method, endpoint string) ([]byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	bodyBytes, _, err := toolscommon.ReadHTTPBodyLimited(resp.Body, toolscommon.DefaultHTTPResponseBytes)
	if err != nil {
		return nil, false, err
	}
	redirected := resp.Request != nil && resp.Request.URL != nil && resp.Request.URL.String() != endpoint
	return bodyBytes, redirected, nil
}

func writeGitHubTestFile(toolCtx toolscore.ToolContext, relativePath string, data []byte) error {
	resolved, err := toolCtx.ResolvePath(relativePath)
	if err != nil {
		return err
	}
	if err := toolCtx.FileSystem().MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return err
	}
	return toolCtx.FileSystem().WriteFile(resolved, data, 0o644)
}

func extractGitHubTestZIP(toolCtx toolscore.ToolContext, relativeRoot string, archive []byte) error {
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return err
	}
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		cleanName := filepath.Clean(file.Name)
		if strings.HasPrefix(cleanName, "..") {
			return fmt.Errorf("unsafe ZIP entry %q", file.Name)
		}
		rc, err := file.Open()
		if err != nil {
			return err
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return err
		}
		if err := writeGitHubTestFile(toolCtx, filepath.ToSlash(filepath.Join(relativeRoot, cleanName)), data); err != nil {
			return err
		}
	}
	return nil
}

type testGitHubRepoToolPlugin struct {
	host ToolHost
}

func (testGitHubRepoToolPlugin) Definition() toolscore.ToolDefinition {
	return toolscore.ToolDefinition{
		Name:        testToolGitHubRepoID,
		Description: "Read GitHub repository metadata through a test-owned GitHub client.",
		Kind:        toolscore.ToolKindFunction,
		Parameters: toolscommon.ObjectSchema(
			map[string]any{
				"action": toolscommon.StringSchema("Read action."),
				"owner":  toolscommon.StringSchema("Repository owner."),
				"repo":   toolscommon.StringSchema("Repository name."),
			},
			"action",
			"owner",
			"repo",
		),
		RequiredArguments: []string{"action", "owner", "repo"},
		RequiresNetwork:   true,
		ReadOnly:          true,
	}
}

func (p testGitHubRepoToolPlugin) NewHandler(config toolscore.ConfiguredTool) (toolscore.ToolHandler, error) {
	_ = config
	return toolscore.ToolHandlerFunc(p.handle), nil
}

func (p testGitHubRepoToolPlugin) handle(ctx context.Context, toolCtx toolscore.ToolContext, step *toolscore.Step, call *toolscore.ToolAction) error {
	if !p.host.NetworkEnabled(toolCtx) {
		toolCtx.SetPolicyError(step, fmt.Errorf("%s requires network.reachable_hosts to be configured", testToolGitHubRepoID))
		return nil
	}
	input, err := toolscore.DecodeToolInput[struct {
		Action string `json:"action"`
		Owner  string `json:"owner"`
		Repo   string `json:"repo"`
	}](call.Input)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	if strings.TrimSpace(input.Action) != "get_repo" {
		toolCtx.SetPolicyError(step, fmt.Errorf("unsupported action %q", strings.TrimSpace(input.Action)))
		return nil
	}
	runtime, client, baseURL, err := testGitHubRequestRuntime(p.host, toolCtx, testToolGitHubRepoID)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	body, _, err := performTestGitHubRequest(
		ctx,
		client,
		runtime.LookupEnv(testToolGitHubTokenEnv),
		http.MethodGet,
		baseURL+"/repos/"+url.PathEscape(strings.TrimSpace(input.Owner))+"/"+url.PathEscape(strings.TrimSpace(input.Repo)),
	)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	toolCtx.SetOutput(step, string(body))
	return nil
}

type testGitHubReviewsToolPlugin struct {
	host ToolHost
}

func (testGitHubReviewsToolPlugin) Definition() toolscore.ToolDefinition {
	return toolscore.ToolDefinition{
		Name:        testToolGitHubReviewID,
		Description: "Read GitHub issue and pull request data through a test-owned GitHub client.",
		Kind:        toolscore.ToolKindFunction,
		Parameters: toolscommon.ObjectSchema(
			map[string]any{
				"action":   toolscommon.StringSchema("Read action."),
				"owner":    toolscommon.StringSchema("Repository owner."),
				"repo":     toolscommon.StringSchema("Repository name."),
				"query":    toolscommon.StringSchema("Search query."),
				"page":     toolscommon.IntegerSchema("Page number."),
				"per_page": toolscommon.IntegerSchema("Page size."),
			},
			"action",
			"owner",
			"repo",
		),
		RequiredArguments: []string{"action", "owner", "repo"},
		RequiresNetwork:   true,
		ReadOnly:          true,
	}
}

func (p testGitHubReviewsToolPlugin) NewHandler(config toolscore.ConfiguredTool) (toolscore.ToolHandler, error) {
	_ = config
	return toolscore.ToolHandlerFunc(p.handle), nil
}

func (p testGitHubReviewsToolPlugin) handle(ctx context.Context, toolCtx toolscore.ToolContext, step *toolscore.Step, call *toolscore.ToolAction) error {
	if !p.host.NetworkEnabled(toolCtx) {
		toolCtx.SetPolicyError(step, fmt.Errorf("%s requires network.reachable_hosts to be configured", testToolGitHubReviewID))
		return nil
	}
	input, err := toolscore.DecodeToolInput[struct {
		Action  string `json:"action"`
		Owner   string `json:"owner"`
		Repo    string `json:"repo"`
		Query   string `json:"query"`
		Page    int    `json:"page"`
		PerPage int    `json:"per_page"`
	}](call.Input)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	if strings.TrimSpace(input.Action) != "search_issues" {
		toolCtx.SetPolicyError(step, fmt.Errorf("unsupported action %q", strings.TrimSpace(input.Action)))
		return nil
	}
	runtime, client, baseURL, err := testGitHubRequestRuntime(p.host, toolCtx, testToolGitHubReviewID)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	target, err := url.Parse(baseURL + "/search/issues")
	if err != nil {
		return err
	}
	query := target.Query()
	page := input.Page
	if page <= 0 {
		page = 1
	}
	perPage := input.PerPage
	if perPage <= 0 {
		perPage = 20
	}
	query.Set("q", strings.TrimSpace(input.Query)+" repo:"+strings.TrimSpace(input.Owner)+"/"+strings.TrimSpace(input.Repo)+" is:issue")
	query.Set("page", strconv.Itoa(page))
	query.Set("per_page", strconv.Itoa(perPage))
	target.RawQuery = query.Encode()
	body, _, err := performTestGitHubRequest(ctx, client, runtime.LookupEnv(testToolGitHubTokenEnv), http.MethodGet, target.String())
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	toolCtx.SetOutput(step, string(body))
	return nil
}

type testGitHubCIToolPlugin struct {
	host ToolHost
}

func (testGitHubCIToolPlugin) Definition() toolscore.ToolDefinition {
	return toolscore.ToolDefinition{
		Name:        testToolGitHubCIID,
		Description: "Read GitHub CI logs and artifacts through a test-owned GitHub client.",
		Kind:        toolscore.ToolKindFunction,
		Parameters: toolscommon.ObjectSchema(
			map[string]any{
				"action":      toolscommon.StringSchema("Read action."),
				"owner":       toolscommon.StringSchema("Repository owner."),
				"repo":        toolscommon.StringSchema("Repository name."),
				"artifact_id": toolscommon.IntegerSchema("Artifact ID."),
				"extract":     toolscommon.BooleanSchema("Whether to extract the archive."),
			},
			"action",
			"owner",
			"repo",
		),
		RequiredArguments: []string{"action", "owner", "repo"},
		RequiresNetwork:   true,
		ReadOnly:          true,
	}
}

func (p testGitHubCIToolPlugin) NewHandler(config toolscore.ConfiguredTool) (toolscore.ToolHandler, error) {
	_ = config
	return toolscore.ToolHandlerFunc(p.handle), nil
}

func (p testGitHubCIToolPlugin) handle(ctx context.Context, toolCtx toolscore.ToolContext, step *toolscore.Step, call *toolscore.ToolAction) error {
	if !p.host.NetworkEnabled(toolCtx) {
		toolCtx.SetPolicyError(step, fmt.Errorf("%s requires network.reachable_hosts to be configured", testToolGitHubCIID))
		return nil
	}
	input, err := toolscore.DecodeToolInput[struct {
		Action     string `json:"action"`
		Owner      string `json:"owner"`
		Repo       string `json:"repo"`
		ArtifactID int64  `json:"artifact_id"`
		Extract    bool   `json:"extract"`
	}](call.Input)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	if strings.TrimSpace(input.Action) != "download_artifact" {
		toolCtx.SetPolicyError(step, fmt.Errorf("unsupported action %q", strings.TrimSpace(input.Action)))
		return nil
	}
	if input.ArtifactID <= 0 {
		toolCtx.SetPolicyError(step, fmt.Errorf("artifact_id must be a positive integer"))
		return nil
	}
	runtime, client, baseURL, err := testGitHubRequestRuntime(p.host, toolCtx, testToolGitHubCIID)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	metadataURL := fmt.Sprintf("%s/repos/%s/%s/actions/artifacts/%d", baseURL, url.PathEscape(strings.TrimSpace(input.Owner)), url.PathEscape(strings.TrimSpace(input.Repo)), input.ArtifactID)
	if _, _, err := performTestGitHubRequest(ctx, client, runtime.LookupEnv(testToolGitHubTokenEnv), http.MethodGet, metadataURL); err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	downloadURL := fmt.Sprintf("%s/repos/%s/%s/actions/artifacts/%d/zip", baseURL, url.PathEscape(strings.TrimSpace(input.Owner)), url.PathEscape(strings.TrimSpace(input.Repo)), input.ArtifactID)
	archiveBytes, redirected, err := performTestGitHubRequest(ctx, client, runtime.LookupEnv(testToolGitHubTokenEnv), http.MethodGet, downloadURL)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	archivePath := filepath.ToSlash(filepath.Join("github", "actions", "artifacts", fmt.Sprintf("%d.zip", input.ArtifactID)))
	if err := writeGitHubTestFile(toolCtx, archivePath, archiveBytes); err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	if input.Extract {
		if err := extractGitHubTestZIP(toolCtx, filepath.ToSlash(filepath.Join("github", "actions", "artifacts", fmt.Sprintf("%d", input.ArtifactID))), archiveBytes); err != nil {
			toolCtx.SetPolicyError(step, err)
			return nil
		}
	}
	output, err := toolscommon.MarshalToolOutput(map[string]any{
		"artifact_id": input.ArtifactID,
		"redirected":  redirected,
	})
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
