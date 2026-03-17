package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
	toolregistry "github.com/richardartoul/swarmd/pkg/tools/registry"
)

// ToolLogger is the generic logging surface that server-owned tools can use.
type ToolLogger interface {
	LogAgentCommand(triggerCtx TriggerContext, level, message string)
}

// ToolRuntime exposes per-agent runtime data to server-owned tools without
// requiring the server package to know about any concrete tool types.
type ToolRuntime interface {
	NamespaceID() string
	AgentID() string
	LookupEnv(name string) string
	Logger() ToolLogger
}

// ToolHost bridges the generic runtime/tool context into server-owned tools.
type ToolHost interface {
	Runtime(toolCtx toolscore.ToolContext) (ToolRuntime, error)
	FileSystem(toolCtx toolscore.ToolContext) sandbox.FileSystem
	ResolvePath(toolCtx toolscore.ToolContext, path string) (string, error)
	NetworkEnabled(toolCtx toolscore.ToolContext) bool
	HTTPClient(toolCtx toolscore.ToolContext, opts toolscore.ToolHTTPClientOptions) *http.Client
	TriggerContext(ctx context.Context) (TriggerContext, bool)
}

// ToolFactory constructs one tool plugin using the server host bridge.
type ToolFactory func(host ToolHost) toolscore.ToolPlugin

// ToolRegistrationOptions configures one server-owned tool registration.
type ToolRegistrationOptions struct {
	RequiredEnv []string
}

// RegisterTool registers one server-owned tool with the shared registry.
func RegisterTool(factory ToolFactory, opts ToolRegistrationOptions) {
	if factory == nil {
		panic("server tool factory must not be nil")
	}
	plugin := factory(toolHostBridge{})
	if err := toolregistry.Register(plugin, toolregistry.RegistrationOptions{
		RequiredEnv: opts.RequiredEnv,
	}); err != nil {
		panic(err)
	}
}

type toolHostBridge struct{}

func (toolHostBridge) Runtime(toolCtx toolscore.ToolContext) (ToolRuntime, error) {
	if toolCtx == nil {
		return nil, fmt.Errorf("tool context is unavailable")
	}
	runtime, ok := toolCtx.RuntimeData().(ToolRuntime)
	if !ok || runtime == nil {
		return nil, fmt.Errorf("server tool runtime is unavailable")
	}
	return runtime, nil
}

func (toolHostBridge) FileSystem(toolCtx toolscore.ToolContext) sandbox.FileSystem {
	if toolCtx == nil {
		return nil
	}
	return toolCtx.FileSystem()
}

func (toolHostBridge) ResolvePath(toolCtx toolscore.ToolContext, path string) (string, error) {
	if toolCtx == nil {
		return "", fmt.Errorf("tool context is unavailable")
	}
	return toolCtx.ResolvePath(path)
}

func (toolHostBridge) NetworkEnabled(toolCtx toolscore.ToolContext) bool {
	return toolCtx != nil && toolCtx.NetworkEnabled()
}

func (toolHostBridge) HTTPClient(toolCtx toolscore.ToolContext, opts toolscore.ToolHTTPClientOptions) *http.Client {
	if toolCtx == nil {
		return nil
	}
	return toolCtx.HTTPClient(opts)
}

func (toolHostBridge) TriggerContext(ctx context.Context) (TriggerContext, bool) {
	trigger, ok := triggerFromContext(ctx)
	if !ok {
		return TriggerContext{}, false
	}
	triggerCtx, err := TriggerContextFromTrigger(trigger)
	if err != nil {
		return TriggerContext{}, false
	}
	return triggerCtx, true
}

type serverToolRuntime struct {
	namespaceID string
	agentID     string
	envLookup   func(string) string
	logger      ToolLogger
}

func newServerToolRuntime(namespaceID, agentID string, envLookup func(string) string, logger ToolLogger) ToolRuntime {
	if envLookup == nil {
		envLookup = os.Getenv
	}
	return serverToolRuntime{
		namespaceID: strings.TrimSpace(namespaceID),
		agentID:     strings.TrimSpace(agentID),
		envLookup:   envLookup,
		logger:      logger,
	}
}

func (r serverToolRuntime) NamespaceID() string {
	return r.namespaceID
}

func (r serverToolRuntime) AgentID() string {
	return r.agentID
}

func (r serverToolRuntime) LookupEnv(name string) string {
	if r.envLookup == nil {
		return ""
	}
	return strings.TrimSpace(r.envLookup(strings.TrimSpace(name)))
}

func (r serverToolRuntime) Logger() ToolLogger {
	return r.logger
}
