package slackreplies

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/richardartoul/swarmd/pkg/server"
	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
)

const (
	toolName              = "slack_replies"
	SlackUserTokenEnvVar  = "SLACK_USER_TOKEN"
	slackAPIBaseURLEnvVar = "SLACK_API_BASE_URL"
)

var registerOnce sync.Once

type config struct {
	DefaultChannel string `json:"default_channel"`
}

type input struct {
	Channel  string `json:"channel"`
	ThreadTS string `json:"thread_ts"`
	AfterTS  string `json:"after_ts"`
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
			RequiredEnv: []string{SlackUserTokenEnvVar},
		})
	})
}

func (plugin) Definition() toolscore.ToolDefinition {
	return toolscore.ToolDefinition{
		Name:        toolName,
		Description: "List Slack replies for one thread through the tool-owned Slack client.",
		Kind:        toolscore.ToolKindFunction,
		Parameters: toolscommon.ObjectSchema(
			map[string]any{
				"channel":   toolscommon.StringSchema("Slack conversation ID. Optional when tools[].config.default_channel is set."),
				"thread_ts": toolscommon.StringSchema("Slack thread ts to read replies from."),
				"after_ts":  toolscommon.StringSchema("Optional cursor. Only replies newer than this ts are returned."),
			},
			"thread_ts",
		),
		RequiredArguments: []string{"thread_ts"},
		Examples: []string{
			`{"channel":"C12345678","thread_ts":"1700.000001"}`,
			`{"thread_ts":"1700.000001","after_ts":"1700.000002"}`,
		},
		OutputNotes:     "Returns JSON with the channel, thread_ts, and reply messages newer than any provided cursor.",
		SafetyTags:      []string{"network", "read_only"},
		RequiresNetwork: true,
		ReadOnly:        true,
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
	if !p.host.NetworkEnabled(toolCtx) {
		toolCtx.SetPolicyError(step, fmt.Errorf("%s requires network.reachable_hosts to be configured", toolName))
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
	result, err := client.ListThreadReplies(ctx, SlackListThreadRepliesParams{
		Channel:  channel,
		ThreadTS: threadTS,
		AfterTS:  strings.TrimSpace(input.AfterTS),
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
