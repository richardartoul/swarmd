package serverlog

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/richardartoul/swarmd/pkg/server"
	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
)

const toolName = "server_log"

var registerOnce sync.Once

type input struct {
	Level   string `json:"level"`
	Message string `json:"message"`
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
		}, server.ToolRegistrationOptions{})
	})
}

func (plugin) Definition() toolscore.ToolDefinition {
	return toolscore.ToolDefinition{
		Name:        toolName,
		Description: "Write a message to the server logs with namespace and agent context.",
		Kind:        toolscore.ToolKindFunction,
		Parameters: toolscommon.ObjectSchema(
			map[string]any{
				"level": map[string]any{
					"type":        "string",
					"description": "Log severity. Must be one of debug, info, warn, or error.",
					"enum":        []string{"debug", "info", "warn", "error"},
				},
				"message": toolscommon.StringSchema("Message text to write to the server logs."),
			},
			"level",
			"message",
		),
		RequiredArguments: []string{"level", "message"},
		Examples: []string{
			`{"level":"info","message":"scheduled heartbeat from server-log-heartbeat"}`,
		},
		OutputNotes: "Returns JSON confirming the level and message that were logged.",
		SafetyTags:  []string{"mutating"},
		Mutating:    true,
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
	level := strings.TrimSpace(input.Level)
	switch level {
	case "debug", "info", "warn", "error":
	default:
		toolCtx.SetPolicyError(step, fmt.Errorf(`level must be one of "debug", "info", "warn", or "error"`))
		return nil
	}
	message := strings.TrimSpace(input.Message)
	if message == "" {
		toolCtx.SetPolicyError(step, fmt.Errorf("message must not be empty"))
		return nil
	}
	runtime, err := p.host.Runtime(toolCtx)
	if err != nil {
		return err
	}
	triggerCtx := server.TriggerContext{
		NamespaceID: runtime.NamespaceID(),
		AgentID:     runtime.AgentID(),
	}
	if current, ok := p.host.TriggerContext(ctx); ok {
		triggerCtx = current
	}
	if logger := runtime.Logger(); logger != nil {
		logger.LogAgentCommand(triggerCtx, level, message)
	}
	output, err := toolscommon.MarshalToolOutput(map[string]any{
		"ok":      true,
		"level":   level,
		"message": message,
	})
	if err != nil {
		return err
	}
	toolCtx.SetOutput(step, output)
	return nil
}
