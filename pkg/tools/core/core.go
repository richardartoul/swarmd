package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/richardartoul/swarmd/pkg/sh/interp"
	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
)

// ConfiguredTool is one explicit tool entry loaded from agent config.
type ConfiguredTool struct {
	ID      string         `json:"id,omitempty"`
	Enabled *bool          `json:"enabled,omitempty"`
	Config  map[string]any `json:"config,omitempty"`
}

func (t ConfiguredTool) EnabledValue() bool {
	return t.Enabled == nil || *t.Enabled
}

func (t ConfiguredTool) Clone() ConfiguredTool {
	cloned := ConfiguredTool{ID: t.ID}
	if t.Enabled != nil {
		value := *t.Enabled
		cloned.Enabled = &value
	}
	if len(t.Config) > 0 {
		cloned.Config = make(map[string]any, len(t.Config))
		for key, value := range t.Config {
			cloned.Config[key] = value
		}
	}
	return cloned
}

// ToolHTTPClientOptions configures runtime-owned HTTP clients exposed to tools.
type ToolHTTPClientOptions struct {
	ConnectTimeout  time.Duration
	FollowRedirects bool
}

// WebSearchResponse is one normalized search result page returned by the host-owned backend.
type WebSearchResponse struct {
	Provider string
	Query    string
	Results  []WebSearchResult
}

// WebSearchResult is one normalized web search result.
type WebSearchResult struct {
	Title   string
	URL     string
	Snippet string
}

// FileReference points at one sandbox-visible file produced or surfaced by a tool.
type FileReference struct {
	Path        string `json:"path"`
	MimeType    string `json:"mime_type,omitempty"`
	Description string `json:"description,omitempty"`
	SizeBytes   int64  `json:"size_bytes,omitempty"`
}

// WebSearchBackend is one runtime-owned provider used by the web_search tool.
type WebSearchBackend interface {
	Search(ctx context.Context, clientFactory interp.HTTPClientFactory, query string, limit int) (WebSearchResponse, error)
}

// ShellExecution describes one host-owned shell execution requested by a tool.
type ShellExecution struct {
	Command   string
	Workdir   string
	TimeoutMS int
}

// Usage reports token accounting from one driver response.
type Usage struct {
	CachedTokens int
}

// ToolKind identifies the model-facing tool wire shape.
type ToolKind string

const (
	ToolKindFunction ToolKind = "function"
	ToolKindCustom   ToolKind = "custom"
)

// ToolFormat describes a custom/freeform tool format.
type ToolFormat struct {
	Type       string `json:"type,omitempty"`
	Syntax     string `json:"syntax,omitempty"`
	Definition string `json:"definition,omitempty"`
}

// ToolBoundaryKind identifies a provider-facing tool shape.
type ToolBoundaryKind string

const (
	ToolBoundaryKindFunction   ToolBoundaryKind = "function"
	ToolBoundaryKindCustom     ToolBoundaryKind = "custom"
	ToolBoundaryKindLocalShell ToolBoundaryKind = "local_shell"
	ToolBoundaryKindWebSearch  ToolBoundaryKind = "web_search"
	ToolBoundaryKindToolSearch ToolBoundaryKind = "tool_search"
)

// ToolInterop captures provider and MCP adaptation metadata for one tool.
type ToolInterop struct {
	MCPToolName            string           `json:"mcp_tool_name,omitempty"`
	OpenAIPreferredKind    ToolBoundaryKind `json:"openai_preferred_kind,omitempty"`
	OpenAIFallbackKind     ToolBoundaryKind `json:"openai_fallback_kind,omitempty"`
	OpenAIFallbackToolName string           `json:"openai_fallback_tool_name,omitempty"`
}

// ToolDefinition is one model-facing tool exposed for the current turn.
type ToolDefinition struct {
	Name              string         `json:"name"`
	Description       string         `json:"description"`
	Kind              ToolKind       `json:"kind"`
	Strict            bool           `json:"strict,omitempty"`
	Parameters        map[string]any `json:"parameters,omitempty"`
	CustomFormat      *ToolFormat    `json:"custom_format,omitempty"`
	RequiredArguments []string       `json:"required_arguments,omitempty"`
	Examples          []string       `json:"examples,omitempty"`
	OutputNotes       string         `json:"output_notes,omitempty"`
	Interop           ToolInterop    `json:"interop,omitempty"`
	SafetyTags        []string       `json:"safety_tags,omitempty"`
	RequiresNetwork   bool           `json:"requires_network,omitempty"`
	ReadOnly          bool           `json:"read_only,omitempty"`
	Mutating          bool           `json:"mutating,omitempty"`
	Fallback          bool           `json:"fallback,omitempty"`
}

// ToolAction asks the runtime to invoke one structured tool.
type ToolAction struct {
	Name  string
	Kind  ToolKind
	Input string
}

// StepStatus classifies one action step outcome.
type StepStatus string

const (
	StepStatusOK          StepStatus = "ok"
	StepStatusExitStatus  StepStatus = "exit_status"
	StepStatusParseError  StepStatus = "parse_error"
	StepStatusPolicyError StepStatus = "policy_error"
	StepStatusFatalError  StepStatus = "fatal_error"
)

// StepType identifies the kind of action recorded in a step.
type StepType string

const (
	StepTypeShell StepType = "shell"
	StepTypeTool  StepType = "tool"
)

// Step records one action execution attempt.
type Step struct {
	Index                 int
	Type                  StepType
	Thought               string
	ActionName            string
	ActionToolKind        ToolKind
	ActionInput           string
	ActionOutput          string
	ActionOutputTruncated bool
	ActionOutputFiles     []FileReference
	ActionOutputBytes     int64
	ActionOutputCompacted bool
	Shell                 string
	Usage                 Usage
	CWDBefore             string
	CWDAfter              string
	Stdout                string
	Stderr                string
	StdoutTruncated       bool
	StderrTruncated       bool
	StdoutFile            *FileReference
	StderrFile            *FileReference
	StdoutBytes           int64
	StderrBytes           int64
	StartedAt             time.Time
	FinishedAt            time.Time
	Duration              time.Duration
	Status                StepStatus
	ExitStatus            int
	Error                 string
}

// ToolContext exposes sandbox-safe runtime capabilities to tool handlers.
type ToolContext interface {
	WorkingDir() string
	FileSystem() sandbox.FileSystem
	ResolvePath(path string) (string, error)
	NetworkEnabled() bool
	HTTPClient(opts ToolHTTPClientOptions) *http.Client
	RuntimeData() any
	StepTimeout() time.Duration
	SearchWeb(ctx context.Context, query string, limit int) (WebSearchResponse, error)
	RunShell(ctx context.Context, step *Step, exec ShellExecution) error
	SetOutput(step *Step, output string)
	SetPolicyError(step *Step, err error)
	SetParseError(step *Step, err error)
}

// ToolHandler executes one tool invocation.
type ToolHandler interface {
	Invoke(ctx context.Context, toolCtx ToolContext, step *Step, call *ToolAction) error
}

// ToolHandlerFunc adapts a function to [ToolHandler].
type ToolHandlerFunc func(ctx context.Context, toolCtx ToolContext, step *Step, call *ToolAction) error

// Invoke implements [ToolHandler].
func (f ToolHandlerFunc) Invoke(ctx context.Context, toolCtx ToolContext, step *Step, call *ToolAction) error {
	return f(ctx, toolCtx, step, call)
}

// ToolPlugin declares one structured tool implementation that can be registered with the runtime.
type ToolPlugin interface {
	Definition() ToolDefinition
	NewHandler(config ConfiguredTool) (ToolHandler, error)
}

// DecodeToolInput decodes one JSON tool input value into T.
func DecodeToolInput[T any](raw string) (T, error) {
	var zero T
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "{}"
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value T
	if err := decoder.Decode(&value); err != nil {
		return zero, fmt.Errorf("invalid tool arguments: %w", err)
	}
	if decoder.More() {
		return zero, fmt.Errorf("invalid tool arguments: trailing data after JSON value")
	}
	return value, nil
}
