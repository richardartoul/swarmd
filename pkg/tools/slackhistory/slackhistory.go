package slackhistory

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/richardartoul/swarmd/pkg/server"
	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
	"github.com/richardartoul/swarmd/pkg/tools/slackcommon"
)

const (
	toolName              = "slack_channel_history"
	SlackUserTokenEnvVar  = slackcommon.SlackUserTokenEnvVar
	slackAPIBaseURLEnvVar = slackcommon.SlackAPIBaseURLEnvVar
)

var registerOnce sync.Once

type config struct {
	DefaultChannel string `json:"default_channel"`
}

type input struct {
	Channel     string `json:"channel"`
	AfterTS     string `json:"after_ts"`
	BeforeTS    string `json:"before_ts"`
	MaxMessages *int   `json:"max_messages"`
}

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
			RequiredEnv:   []string{SlackUserTokenEnvVar},
			RequiredHosts: slackcommon.RequiredHosts(),
		})
	})
}

func (plugin) Definition() toolscore.ToolDefinition {
	maxMessagesSchema := toolscommon.IntegerSchema("Optional maximum number of timeline messages to return before truncating the result.")
	maxMessagesSchema["minimum"] = 1
	return toolscore.ToolDefinition{
		Name:        toolName,
		Description: "List Slack channel timeline messages newer than a given timestamp through the tool-owned Slack client.",
		Kind:        toolscore.ToolKindFunction,
		Parameters: toolscommon.ObjectSchema(
			map[string]any{
				"channel":      toolscommon.StringSchema("Slack conversation ID. Optional when tools[].config.default_channel is set."),
				"after_ts":     toolscommon.StringSchema("Exclusive lower bound. Only messages newer than this Slack timestamp are returned."),
				"before_ts":    toolscommon.StringSchema("Optional exclusive upper bound. Only messages older than this Slack timestamp are returned."),
				"max_messages": maxMessagesSchema,
			},
			"after_ts",
		),
		RequiredArguments: []string{"after_ts"},
		Examples: []string{
			`{"channel":"C12345678","after_ts":"1700.000001"}`,
			`{"after_ts":"1700.000001","before_ts":"1700.000100","max_messages":25}`,
		},
		OutputNotes:  "Returns chronological channel timeline messages with truncation metadata when max_messages stops pagination early.",
		SafetyTags:   []string{"network", "read_only"},
		NetworkScope: toolscore.ToolNetworkScopeScoped,
		ReadOnly:     true,
	}
}

func (p plugin) NewHandler(raw toolscore.ConfiguredTool) (toolscore.ToolHandler, error) {
	cfg, err := toolscommon.DecodeToolConfig[config](raw.Config)
	if err != nil {
		return nil, fmt.Errorf("decode %s config: %w", toolName, err)
	}
	cfg.DefaultChannel = strings.TrimSpace(cfg.DefaultChannel)
	return toolscore.ToolHandlerFunc(func(ctx context.Context, toolCtx toolscore.ToolContext, step *toolscore.Step, call *toolscore.ToolAction) error {
		return p.handle(ctx, toolCtx, step, call, cfg)
	}), nil
}

func (p plugin) handle(ctx context.Context, toolCtx toolscore.ToolContext, step *toolscore.Step, call *toolscore.ToolAction, cfg config) error {
	input, err := toolscore.DecodeToolInput[input](call.Input)
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
	beforeTS := strings.TrimSpace(input.BeforeTS)
	if beforeTS != "" && slackcommon.CompareTimestamp(beforeTS, afterTS) <= 0 {
		toolCtx.SetPolicyError(step, fmt.Errorf("before_ts must be greater than after_ts"))
		return nil
	}
	maxMessages := 0
	if input.MaxMessages != nil {
		if *input.MaxMessages <= 0 {
			toolCtx.SetPolicyError(step, fmt.Errorf("max_messages must be greater than 0"))
			return nil
		}
		maxMessages = *input.MaxMessages
	}

	runtime, err := p.host.Runtime(toolCtx)
	if err != nil {
		return err
	}
	client, err := NewSlackClient(SlackClientConfig{
		Token:   runtime.LookupEnv(SlackUserTokenEnvVar),
		BaseURL: runtime.LookupEnv(slackAPIBaseURLEnvVar),
		Client: p.host.HTTPClient(toolCtx, toolscore.ToolHTTPClientOptions{
			ConnectTimeout:  10 * time.Second,
			FollowRedirects: true,
		}),
	})
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}

	result, err := client.ListChannelHistory(ctx, SlackListChannelHistoryParams{
		Channel:     channel,
		AfterTS:     afterTS,
		BeforeTS:    beforeTS,
		MaxMessages: maxMessages,
	})
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
