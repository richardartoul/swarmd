package runshell

import (
	"context"
	"fmt"
	"strings"
	"sync"

	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
	toolregistry "github.com/richardartoul/swarmd/pkg/tools/registry"
)

const toolName = "run_shell"

var registerOnce sync.Once

type args struct {
	Command   string `json:"command"`
	Workdir   string `json:"workdir"`
	TimeoutMS int    `json:"timeout_ms"`
}

type plugin struct{}

func init() {
	Register()
}

func Register() {
	registerOnce.Do(func() {
		toolregistry.MustRegister(plugin{}, toolregistry.RegistrationOptions{BuiltIn: true})
	})
}

func (plugin) Definition() toolscore.ToolDefinition {
	return toolscore.ToolDefinition{
		Name:        toolName,
		Description: "Runs one sandboxed shell command when no structured tool fits.",
		Kind:        toolscore.ToolKindFunction,
		Parameters: toolscommon.ObjectSchema(
			map[string]any{
				"command":    toolscommon.StringSchema("The POSIX shell snippet to execute."),
				"workdir":    toolscommon.StringSchema("Optional working directory for the command."),
				"timeout_ms": toolscommon.NumberSchema("Optional timeout in milliseconds."),
			},
			"command",
		),
		RequiredArguments: []string{"command"},
		Examples: []string{
			`{"command":"pwd && ls -la","timeout_ms":10000}`,
		},
		OutputNotes: "Returns shell-like execution summaries with working directory, stdout, stderr, exit status, and truncation notices.",
		Interop: toolscommon.ToolInterop(
			toolName,
			toolscore.ToolBoundaryKindLocalShell,
			toolscore.ToolBoundaryKindFunction,
			toolName,
		),
		SafetyTags: []string{"fallback"},
		Fallback:   true,
	}
}

func (plugin) NewHandler(config toolscore.ConfiguredTool) (toolscore.ToolHandler, error) {
	if err := toolscommon.ValidateNoToolConfig(toolName, config.Config); err != nil {
		return nil, err
	}
	return toolscore.ToolHandlerFunc(handle), nil
}

func handle(ctx context.Context, toolCtx toolscore.ToolContext, step *toolscore.Step, call *toolscore.ToolAction) error {
	args, err := toolscore.DecodeToolInput[args](call.Input)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	if strings.TrimSpace(args.Command) == "" {
		toolCtx.SetPolicyError(step, fmt.Errorf("command must not be empty"))
		return nil
	}
	return toolCtx.RunShell(ctx, step, toolscore.ShellExecution{
		Command:   args.Command,
		Workdir:   args.Workdir,
		TimeoutMS: args.TimeoutMS,
	})
}
