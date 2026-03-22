package slackpost

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
	toolName              = "slack_post"
	SlackUserTokenEnvVar  = slackcommon.SlackUserTokenEnvVar
	slackAPIBaseURLEnvVar = slackcommon.SlackAPIBaseURLEnvVar
)

var registerOnce sync.Once

type config struct {
	DefaultChannel string `json:"default_channel"`
}

type input struct {
	Channel  string `json:"channel"`
	Text     string `json:"text"`
	ThreadTS string `json:"thread_ts"`
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
	return toolscore.ToolDefinition{
		Name:        toolName,
		Description: "Post a Slack message or thread reply through the tool-owned Slack client.",
		Kind:        toolscore.ToolKindFunction,
		Parameters: toolscommon.ObjectSchema(
			map[string]any{
				"channel":   toolscommon.StringSchema("Slack conversation ID. Optional when tools[].config.default_channel is set."),
				"text":      toolscommon.StringSchema("Message text to send."),
				"thread_ts": toolscommon.StringSchema("Optional Slack thread ts to reply to. Omit to create a new root message."),
			},
			"text",
		),
		RequiredArguments: []string{"text"},
		Examples: []string{
			`{"text":"Build finished successfully."}`,
			`{"channel":"C12345678","thread_ts":"1700.000001","text":"Following up in-thread."}`,
		},
		OutputNotes:  "Returns JSON with the Slack channel, ts, and thread_ts for the posted message.",
		SafetyTags:   []string{"network", "mutating"},
		NetworkScope: toolscore.ToolNetworkScopeScoped,
		Mutating:     true,
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
	text := strings.TrimSpace(input.Text)
	if text == "" {
		toolCtx.SetPolicyError(step, fmt.Errorf("text must not be empty"))
		return nil
	}
	channel := toolscommon.FirstNonEmptyString(input.Channel, cfg.DefaultChannel)
	if channel == "" {
		toolCtx.SetPolicyError(step, fmt.Errorf(`channel must be provided via "channel" or tools[].config.default_channel`))
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
	result, err := client.PostMessage(ctx, SlackPostMessageParams{
		Channel:  channel,
		Text:     text,
		ThreadTS: strings.TrimSpace(input.ThreadTS),
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
