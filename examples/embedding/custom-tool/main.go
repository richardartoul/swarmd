package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/richardartoul/swarmd/pkg/agent"
	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
)

const toolName = "lookup_service_owner"

type lookupTool struct{}

type lookupToolConfig struct {
	Label string `json:"label"`
}

type lookupInput struct {
	Service string `json:"service"`
}

type lookupOutput struct {
	Service string `json:"service"`
	Label   string `json:"label"`
	Owner   string `json:"owner"`
	Catalog string `json:"catalog"`
}

type runtimeCatalog struct {
	Name   string
	Owners map[string]string
}

func main() {
	agent.RegisterTool(lookupTool{})

	root, err := os.MkdirTemp("", "swarmd-custom-tool")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(root)

	runtime, err := agent.New(agent.Config{
		Root: root,
		ConfiguredTools: []agent.ConfiguredTool{{
			ID: toolName,
			Config: map[string]any{
				"label": "team",
			},
		}},
		ToolRuntimeData: runtimeCatalog{
			Name: "demo-service-catalog",
			Owners: map[string]string{
				"billing": "payments-platform",
				"search":  "developer-experience",
			},
		},
		Driver: agent.DriverFunc(func(_ context.Context, req agent.Request) (agent.Decision, error) {
			if req.Step == 1 {
				return agent.Decision{
					Tool: &agent.ToolAction{
						Name:  toolName,
						Kind:  agent.ToolKindFunction,
						Input: `{"service":"billing"}`,
					},
				}, nil
			}
			return agent.Decision{
				Finish: &agent.FinishAction{Value: "lookup complete"},
			}, nil
		}),
		SystemPrompt: "demo prompt",
	})
	if err != nil {
		panic(err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			panic(err)
		}
	}()

	result, err := runtime.HandleTrigger(context.Background(), agent.Trigger{Kind: "example"})
	if err != nil {
		panic(err)
	}
	if len(result.Steps) == 0 {
		panic("expected at least one step")
	}

	var output lookupOutput
	if err := json.Unmarshal([]byte(result.Steps[0].ActionOutput), &output); err != nil {
		panic(err)
	}

	fmt.Printf("service: %s\n", output.Service)
	fmt.Printf("%s: %s\n", output.Label, output.Owner)
	fmt.Printf("catalog: %s\n", output.Catalog)
	fmt.Printf("value: %v\n", result.Value)
}

func (lookupTool) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        toolName,
		Description: "Look up the owner of one service from host-provided runtime data.",
		Kind:        agent.ToolKindFunction,
		Parameters: toolscommon.ObjectSchema(
			map[string]any{
				"service": toolscommon.StringSchema("Service name to look up."),
			},
			"service",
		),
		RequiredArguments: []string{"service"},
		ReadOnly:          true,
	}
}

func (lookupTool) NewHandler(cfg agent.ConfiguredTool) (agent.ToolHandler, error) {
	parsed := lookupToolConfig{Label: "owner"}
	if len(cfg.Config) > 0 {
		raw, err := json.Marshal(cfg.Config)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return nil, fmt.Errorf("decode %s config: %w", toolName, err)
		}
	}
	parsed.Label = strings.TrimSpace(parsed.Label)
	if parsed.Label == "" {
		parsed.Label = "owner"
	}

	return agent.ToolHandlerFunc(func(_ context.Context, toolCtx agent.ToolContext, step *agent.Step, call *agent.ToolAction) error {
		input, err := agent.DecodeToolInput[lookupInput](call.Input)
		if err != nil {
			toolCtx.SetPolicyError(step, err)
			return nil
		}

		data, ok := toolCtx.RuntimeData().(runtimeCatalog)
		if !ok {
			return fmt.Errorf("unexpected runtime data type %T", toolCtx.RuntimeData())
		}

		service := strings.TrimSpace(input.Service)
		owner := strings.TrimSpace(data.Owners[service])
		if owner == "" {
			toolCtx.SetPolicyError(step, fmt.Errorf("unknown service %q", service))
			return nil
		}

		output, err := toolscommon.MarshalToolOutput(lookupOutput{
			Service: service,
			Label:   parsed.Label,
			Owner:   owner,
			Catalog: data.Name,
		})
		if err != nil {
			return err
		}
		toolCtx.SetOutput(step, output)
		return nil
	}), nil
}
