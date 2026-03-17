// See LICENSE for licensing information

package agent

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/richardartoul/swarmd/pkg/sh/interp"
	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
	_ "github.com/richardartoul/swarmd/pkg/tools/builtin"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
	toolregistry "github.com/richardartoul/swarmd/pkg/tools/registry"
)

type ConfiguredTool = toolscore.ConfiguredTool
type ToolHTTPClientOptions = toolscore.ToolHTTPClientOptions
type ToolContext = toolscore.ToolContext
type ToolHandler = toolscore.ToolHandler
type ToolHandlerFunc = toolscore.ToolHandlerFunc
type ToolPlugin = toolscore.ToolPlugin

type runtimeToolContext struct {
	agent   *Agent
	stepNum int
}

// WorkingDir returns the sandbox working directory for the current step.
func (c runtimeToolContext) WorkingDir() string {
	if c.agent == nil || c.agent.runner == nil {
		return ""
	}
	return c.agent.runner.Dir
}

// FileSystem returns the sandbox-rooted filesystem used by this agent.
func (c runtimeToolContext) FileSystem() sandbox.FileSystem {
	if c.agent == nil {
		return nil
	}
	return c.agent.fileSystem
}

// ResolvePath resolves a sandbox-visible path relative to the current working directory.
func (c runtimeToolContext) ResolvePath(path string) (string, error) {
	if c.agent == nil {
		return "", fmt.Errorf("tool context is unavailable")
	}
	return c.agent.resolveToolPath(path)
}

// NetworkEnabled reports whether runtime-owned network access is enabled.
func (c runtimeToolContext) NetworkEnabled() bool {
	return c.agent != nil && c.agent.networkEnabled
}

// HTTPClient returns an HTTP client backed by the runtime-owned dialer and header rules.
func (c runtimeToolContext) HTTPClient(opts ToolHTTPClientOptions) *http.Client {
	if c.agent == nil || c.agent.httpClientFactory == nil {
		return nil
	}
	return c.agent.httpClientFactory.NewClient(interp.HTTPClientOptions{
		ConnectTimeout:  opts.ConnectTimeout,
		FollowRedirects: opts.FollowRedirects,
	})
}

// RuntimeData returns host-owned, per-agent runtime data that structured tool handlers can type-assert.
func (c runtimeToolContext) RuntimeData() any {
	if c.agent == nil {
		return nil
	}
	return c.agent.toolRuntimeData
}

func (c runtimeToolContext) StepTimeout() time.Duration {
	if c.agent == nil {
		return 0
	}
	return c.agent.stepTimeout
}

func (c runtimeToolContext) SearchWeb(ctx context.Context, query string, limit int) (toolscore.WebSearchResponse, error) {
	if c.agent == nil || c.agent.webSearchBackend == nil {
		return toolscore.WebSearchResponse{}, fmt.Errorf("web_search backend is not configured")
	}
	response, err := c.agent.webSearchBackend.Search(ctx, c.agent.httpClientFactory, query, limit)
	if err != nil {
		return toolscore.WebSearchResponse{}, err
	}
	results := make([]toolscore.WebSearchResult, 0, len(response.Results))
	for _, result := range response.Results {
		results = append(results, toolscore.WebSearchResult{
			Title:   result.Title,
			URL:     result.URL,
			Snippet: result.Snippet,
		})
	}
	return toolscore.WebSearchResponse{
		Provider: response.Provider,
		Query:    response.Query,
		Results:  results,
	}, nil
}

func (c runtimeToolContext) RunShell(ctx context.Context, step *Step, exec toolscore.ShellExecution) error {
	if c.agent == nil {
		return fmt.Errorf("tool context is unavailable")
	}
	if step == nil {
		return fmt.Errorf("step must not be nil")
	}
	return c.agent.runShellTool(ctx, c.stepNum, step, exec)
}

// SetOutput records tool output subject to the agent's output limits.
func (c runtimeToolContext) SetOutput(step *Step, output string) {
	if c.agent != nil {
		c.agent.setToolOutput(step, output)
	}
}

// SetPolicyError records a non-fatal tool policy error.
func (c runtimeToolContext) SetPolicyError(step *Step, err error) {
	setToolPolicyError(step, err)
}

// SetParseError records a non-fatal tool parse error.
func (c runtimeToolContext) SetParseError(step *Step, err error) {
	setToolParseError(step, err)
}

// RegisterTool registers one explicit custom tool. It panics on invalid or duplicate registrations.
func RegisterTool(plugin ToolPlugin) {
	if err := toolregistry.Register(plugin, toolregistry.RegistrationOptions{}); err != nil {
		panic(err)
	}
}

// BuiltInToolNames returns the built-in tool catalog in stable order.
func BuiltInToolNames() []string {
	return toolregistry.BuiltInToolNames()
}

// DecodeToolInput decodes one JSON tool input value into T.
func DecodeToolInput[T any](raw string) (T, error) {
	return toolscore.DecodeToolInput[T](raw)
}

// NormalizeConfiguredTools validates and normalizes explicit custom tool references.
func NormalizeConfiguredTools(tools []ConfiguredTool) ([]ConfiguredTool, error) {
	return toolregistry.NormalizeConfiguredTools(tools)
}

// ResolveToolDefinitions returns the built-in tool surface plus any explicitly configured custom tools.
func ResolveToolDefinitions(tools []ConfiguredTool, networkEnabled bool) ([]ToolDefinition, error) {
	return toolregistry.ResolveToolDefinitions(tools, networkEnabled)
}

// ResolveActionSchema returns a stable, JSON-marshalable schema document for the tool surface exposed to an agent.
func ResolveActionSchema(tools []ConfiguredTool, networkEnabled bool) (map[string]any, error) {
	return toolregistry.ResolveActionSchema(tools, networkEnabled)
}

func resolveToolBindings(tools []ConfiguredTool, networkEnabled bool) ([]toolregistry.ResolvedToolBinding, error) {
	return toolregistry.ResolveToolBindings(tools, networkEnabled)
}
