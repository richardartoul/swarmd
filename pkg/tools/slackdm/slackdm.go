package slackdm

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
	toolName              = "slack_dm"
	SlackUserTokenEnvVar  = slackcommon.SlackUserTokenEnvVar
	slackAPIBaseURLEnvVar = slackcommon.SlackAPIBaseURLEnvVar
)

var registerOnce sync.Once

type input struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Text   string `json:"text"`
}

type SlackDirectMessageResult struct {
	UserID   string `json:"user_id"`
	Email    string `json:"email,omitempty"`
	Channel  string `json:"channel"`
	TS       string `json:"ts"`
	ThreadTS string `json:"thread_ts"`
}

type plugin struct {
	host  server.ToolHost
	cache *emailUserIDCache
}

func init() {
	Register()
}

func Register() {
	registerOnce.Do(func() {
		server.RegisterTool(func(host server.ToolHost) toolscore.ToolPlugin {
			return &plugin{
				host:  host,
				cache: newEmailUserIDCache(),
			}
		}, server.ToolRegistrationOptions{
			RequiredEnv: []string{SlackUserTokenEnvVar},
		})
	})
}

func (*plugin) Definition() toolscore.ToolDefinition {
	parameters := toolscommon.ObjectSchema(
		map[string]any{
			"user_id": toolscommon.StringSchema("Slack user ID to DM. Provide exactly one of user_id or email."),
			"email":   toolscommon.StringSchema("Slack user email to resolve before DMing. Provide exactly one of user_id or email."),
			"text":    toolscommon.StringSchema("Message text to send."),
		},
		"text",
	)
	parameters["oneOf"] = []map[string]any{
		{"required": []string{"user_id"}},
		{"required": []string{"email"}},
	}
	return toolscore.ToolDefinition{
		Name:        toolName,
		Description: "Send a Slack direct message to one user by user ID or email through the tool-owned Slack client.",
		Kind:        toolscore.ToolKindFunction,
		Parameters:  parameters,
		RequiredArguments: []string{
			"text",
		},
		Examples: []string{
			`{"user_id":"U12345678","text":"Build finished successfully."}`,
			`{"email":"ada@example.com","text":"Can you take a look at the deployment?"}`,
		},
		OutputNotes:     "Returns JSON with the resolved user_id, DM channel, ts, and thread_ts for the posted message.",
		SafetyTags:      []string{"network", "mutating"},
		RequiresNetwork: true,
		Mutating:        true,
	}
}

func (p *plugin) NewHandler(raw toolscore.ConfiguredTool) (toolscore.ToolHandler, error) {
	if err := toolscommon.ValidateNoToolConfig(toolName, raw.Config); err != nil {
		return nil, err
	}
	return toolscore.ToolHandlerFunc(p.handle), nil
}

func (p *plugin) handle(ctx context.Context, toolCtx toolscore.ToolContext, step *toolscore.Step, call *toolscore.ToolAction) error {
	input, err := toolscore.DecodeToolInput[input](call.Input)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	if !p.host.NetworkEnabled(toolCtx) {
		toolCtx.SetPolicyError(step, fmt.Errorf("%s requires network.reachable_hosts to be configured", toolName))
		return nil
	}
	text := strings.TrimSpace(input.Text)
	if text == "" {
		toolCtx.SetPolicyError(step, fmt.Errorf("text must not be empty"))
		return nil
	}
	userID := strings.TrimSpace(input.UserID)
	email := normalizeEmail(input.Email)
	switch {
	case userID == "" && email == "":
		toolCtx.SetPolicyError(step, fmt.Errorf("exactly one of user_id or email must be provided"))
		return nil
	case userID != "" && email != "":
		toolCtx.SetPolicyError(step, fmt.Errorf("exactly one of user_id or email must be provided"))
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
	resolvedUserID := userID
	if email != "" {
		if cachedUserID, ok := p.cache.Get(email); ok {
			resolvedUserID = cachedUserID
		} else {
			lookupResult, err := client.LookupUserByEmail(ctx, email)
			if err != nil {
				toolCtx.SetPolicyError(step, err)
				return nil
			}
			resolvedUserID = lookupResult.UserID
			if lookupResult.Email != "" {
				email = lookupResult.Email
			}
			p.cache.Put(email, resolvedUserID)
		}
	}
	openDMResult, err := client.OpenDM(ctx, resolvedUserID)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	postResult, err := client.PostMessage(ctx, SlackPostMessageParams{
		Channel: openDMResult.Channel,
		Text:    text,
	})
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	output, err := toolscommon.MarshalToolOutput(SlackDirectMessageResult{
		UserID:   resolvedUserID,
		Email:    email,
		Channel:  postResult.Channel,
		TS:       postResult.TS,
		ThreadTS: postResult.ThreadTS,
	})
	if err != nil {
		return err
	}
	toolCtx.SetOutput(step, output)
	return nil
}
