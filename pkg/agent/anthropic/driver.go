// See LICENSE for licensing information

// Package anthropic provides an Anthropic-backed implementation of [agent.Driver].
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/richardartoul/swarmd/pkg/agent"
)

const (
	// DefaultBaseURL is the default Anthropic API base URL.
	DefaultBaseURL = "https://api.anthropic.com/v1"

	// DefaultSystemPrompt is kept for backward compatibility.
	DefaultSystemPrompt = agent.DefaultSystemPrompt

	// DefaultMaxTokens is the built-in Anthropic output budget used by this driver.
	DefaultMaxTokens = 64000

	anthropicVersion = "2023-06-01"
)

// Config configures a new Anthropic-backed driver.
type Config struct {
	APIKey     string
	BaseURL    string
	Model      string
	HTTPClient *http.Client

	// Deprecated: configure the prompt on [agent.Config].
	SystemPrompt string

	// Deprecated: configure history preservation on [agent.Config].
	PreserveConversation bool
}

// Driver implements [agent.Driver] using the Anthropic Messages API.
type Driver struct {
	apiKey    string
	baseURL   string
	model     string
	reasoning string
	client    *http.Client
	maxTokens int
}

type messagesRequest struct {
	Model        string                 `json:"model"`
	MaxTokens    int                    `json:"max_tokens"`
	System       string                 `json:"system,omitempty"`
	Messages     []anthropicMessage     `json:"messages"`
	Thinking     *anthropicThinking     `json:"thinking,omitempty"`
	OutputConfig *anthropicOutputConfig `json:"output_config,omitempty"`
	Tools        []anthropicTool        `json:"tools,omitempty"`
	ToolChoice   *anthropicToolChoice   `json:"tool_choice,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type anthropicThinking struct {
	Type string `json:"type"`
}

type anthropicOutputConfig struct {
	Effort string `json:"effort,omitempty"`
}

type anthropicRequestTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicRequestToolUseBlock struct {
	Type  string         `json:"type"`
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

type anthropicRequestToolResultBlock struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
}

type anthropicTool struct {
	Name          string           `json:"name"`
	Description   string           `json:"description,omitempty"`
	Strict        bool             `json:"strict,omitempty"`
	InputSchema   map[string]any   `json:"input_schema"`
	InputExamples []map[string]any `json:"input_examples,omitempty"`
}

type anthropicToolChoice struct {
	Type                   string `json:"type"`
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use,omitempty"`
}

type messagesResponse struct {
	Content    []anthropicContentBlock `json:"content"`
	StopReason string                  `json:"stop_reason"`
	Usage      anthropicUsage          `json:"usage"`
}

type anthropicContentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type anthropicUsage struct {
	CacheReadInputTokens int `json:"cache_read_input_tokens"`
}

type apiErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

type structuredFinishResponse struct {
	Type    string          `json:"type"`
	Thought string          `json:"thought"`
	Result  json.RawMessage `json:"result"`
}

type decisionResponse struct {
	Type    string          `json:"type"`
	Thought string          `json:"thought"`
	Shell   string          `json:"shell"`
	Tool    string          `json:"tool"`
	Name    string          `json:"name"`
	Args    json.RawMessage `json:"args"`
	Input   string          `json:"input"`
	Result  json.RawMessage `json:"result"`
}

type boundaryToolCallResponse struct {
	Type      string                 `json:"type"`
	Name      string                 `json:"name"`
	Arguments string                 `json:"arguments"`
	Input     string                 `json:"input"`
	CallID    string                 `json:"call_id"`
	Action    *boundaryLocalShellRun `json:"action"`
}

type boundaryLocalShellRun struct {
	Type             string   `json:"type"`
	Command          []string `json:"command"`
	WorkingDirectory string   `json:"working_directory"`
	TimeoutMS        int      `json:"timeout_ms"`
}

// New constructs an Anthropic-backed driver.
func New(cfg Config) (*Driver, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("anthropic api key must not be empty")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, fmt.Errorf("anthropic model must not be empty")
	}
	if strings.TrimSpace(cfg.SystemPrompt) != "" {
		return nil, fmt.Errorf("anthropic system prompt is configured on agent.Config, not anthropic.Config")
	}
	if cfg.PreserveConversation {
		return nil, fmt.Errorf("anthropic conversation history is configured on agent.Config, not anthropic.Config")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	model, reasoning := parseModelAndReasoningLevel(cfg.Model)
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	return &Driver{
		apiKey:    cfg.APIKey,
		baseURL:   baseURL,
		model:     model,
		reasoning: reasoning,
		client:    client,
		maxTokens: DefaultMaxTokens,
	}, nil
}

// Next implements [agent.Driver].
func (d *Driver) Next(ctx context.Context, req agent.Request) (agent.Decision, error) {
	if len(req.Messages) == 0 {
		return agent.Decision{}, fmt.Errorf("anthropic request must include at least one message")
	}
	payload, err := d.buildMessagesRequest(req)
	if err != nil {
		return agent.Decision{}, err
	}
	return d.complete(ctx, payload, req.Tools)
}

func (d *Driver) buildMessagesRequest(req agent.Request) (messagesRequest, error) {
	payload := messagesRequest{
		Model:     d.model,
		MaxTokens: d.maxTokens,
	}
	if d.reasoning != "" {
		payload.Thinking = &anthropicThinking{Type: "adaptive"}
		payload.OutputConfig = &anthropicOutputConfig{Effort: d.reasoning}
	}
	system, messages, err := buildAnthropicMessages(req)
	if err != nil {
		return messagesRequest{}, err
	}
	payload.System = system
	payload.Messages = messages
	if len(payload.Messages) == 0 {
		return messagesRequest{}, fmt.Errorf("anthropic request must include at least one non-system message")
	}
	if len(req.Tools) > 0 {
		payload.Tools = buildAnthropicTools(req.Tools)
		payload.ToolChoice = &anthropicToolChoice{
			Type:                   "auto",
			DisableParallelToolUse: true,
		}
	}
	return payload, nil
}

func buildAnthropicMessages(req agent.Request) (string, []anthropicMessage, error) {
	if len(req.Messages) == 0 {
		return "", nil, nil
	}

	var systemParts []string
	prefix, footer := splitAnthropicRequestMessages(req.Messages)
	messages := make([]anthropicMessage, 0, len(req.Messages)+len(req.Steps)*2)
	appendPrepared := func(message agent.Message) error {
		switch message.Role {
		case agent.MessageRoleSystem:
			if strings.TrimSpace(message.Content) != "" {
				systemParts = append(systemParts, message.Content)
			}
		case agent.MessageRoleUser, agent.MessageRoleAssistant:
			messages = append(messages, anthropicMessage{
				Role:    message.Role,
				Content: message.Content,
			})
		default:
			return fmt.Errorf("unsupported anthropic message role %q", message.Role)
		}
		return nil
	}

	for _, message := range prefix {
		if err := appendPrepared(message); err != nil {
			return "", nil, err
		}
	}

	for _, replay := range agent.BuildStepReplays(req.Steps) {
		replayMessages, err := buildAnthropicReplayMessages(replay, req.Tools)
		if err != nil {
			return "", nil, err
		}
		messages = append(messages, replayMessages...)
	}

	if err := appendPrepared(footer); err != nil {
		return "", nil, err
	}
	return strings.Join(systemParts, "\n\n"), messages, nil
}

func splitAnthropicRequestMessages(messages []agent.Message) ([]agent.Message, agent.Message) {
	if len(messages) == 0 {
		return nil, agent.Message{}
	}
	return messages[:len(messages)-1], messages[len(messages)-1]
}

func buildAnthropicReplayMessages(replay agent.StepReplay, allowedTools []agent.ToolDefinition) ([]anthropicMessage, error) {
	def, ok := allowedToolDefinition(allowedTools, replay.ToolName)
	if !ok {
		def = agent.ToolDefinition{
			Name: replay.ToolName,
			Kind: replay.ToolKind,
		}
	}
	input, err := anthropicReplayToolInput(replay, def)
	if err != nil {
		return nil, err
	}

	assistantContent := make([]any, 0, 2)
	if replay.Thought != "" {
		assistantContent = append(assistantContent, anthropicRequestTextBlock{
			Type: "text",
			Text: replay.Thought,
		})
	}
	assistantContent = append(assistantContent, anthropicRequestToolUseBlock{
		Type:  "tool_use",
		ID:    replay.CallID,
		Name:  def.Name,
		Input: input,
	})

	return []anthropicMessage{
		{
			Role:    agent.MessageRoleAssistant,
			Content: assistantContent,
		},
		{
			Role: agent.MessageRoleUser,
			Content: []any{anthropicRequestToolResultBlock{
				Type:      "tool_result",
				ToolUseID: replay.CallID,
				Content:   replay.Output,
			}},
		},
	}, nil
}

func anthropicReplayToolInput(replay agent.StepReplay, def agent.ToolDefinition) (map[string]any, error) {
	if replay.ToolKind == agent.ToolKindCustom {
		return map[string]any{
			anthropicCustomToolInputField(def): replay.Input,
		}, nil
	}
	raw := strings.TrimSpace(replay.Input)
	if raw == "" {
		return map[string]any{}, nil
	}
	var payload any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, fmt.Errorf("could not decode replay input for tool %q: %w", replay.ToolName, err)
	}
	object, ok := payload.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("replay input for tool %q must decode to an object", replay.ToolName)
	}
	return object, nil
}

func (d *Driver) complete(ctx context.Context, payload messagesRequest, allowedTools []agent.ToolDefinition) (agent.Decision, error) {
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return agent.Decision{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+"/messages", &body)
	if err != nil {
		return agent.Decision{}, err
	}
	req.Header.Set("x-api-key", d.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.client.Do(req)
	if err != nil {
		return agent.Decision{}, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return agent.Decision{}, err
	}
	if resp.StatusCode/100 != 2 {
		var apiErr apiErrorResponse
		if err := json.Unmarshal(respBody, &apiErr); err == nil && apiErr.Error.Message != "" {
			return agent.Decision{}, fmt.Errorf("anthropic api error (%s): %s", resp.Status, apiErr.Error.Message)
		}
		return agent.Decision{}, fmt.Errorf("anthropic api error (%s): %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	var response messagesResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return agent.Decision{}, err
	}
	decision, err := parseMessageDecision(response, allowedTools)
	if err != nil {
		return agent.Decision{}, err
	}
	decision.Usage = agent.Usage{
		CachedTokens: response.Usage.CacheReadInputTokens,
	}
	return decision, nil
}

func parseMessageDecision(response messagesResponse, allowedTools []agent.ToolDefinition) (agent.Decision, error) {
	text := extractAnthropicText(response.Content)
	var toolUses []anthropicContentBlock
	for _, block := range response.Content {
		if block.Type == "tool_use" {
			toolUses = append(toolUses, block)
		}
	}
	if len(toolUses) > 1 {
		return agent.Decision{}, fmt.Errorf("anthropic response must include exactly one tool use when calling tools")
	}
	if len(toolUses) == 1 {
		decision, err := parseToolUseDecision(toolUses[0], allowedTools)
		if err != nil {
			return agent.Decision{}, err
		}
		if text != "" {
			decision.Thought = text
		}
		return decision, nil
	}
	if text != "" {
		return parseDecision(text, allowedTools)
	}
	stopReason := strings.TrimSpace(response.StopReason)
	if stopReason == "" {
		return agent.Decision{}, fmt.Errorf("anthropic response content was empty")
	}
	return agent.Decision{}, fmt.Errorf("anthropic response with stop_reason %q did not include text or tool use", stopReason)
}

func parseDecision(content string, allowedTools []agent.ToolDefinition) (agent.Decision, error) {
	content = unwrapCodeFence(strings.TrimSpace(content))
	if content == "" {
		return agent.Decision{}, fmt.Errorf("anthropic response content was empty")
	}
	if decision, ok, err := parseBoundaryToolDecision(content, allowedTools); ok || err != nil {
		return decision, err
	}
	if decision, ok, err := parseLegacyDecision(content, allowedTools); ok || err != nil {
		return decision, err
	}
	if decision, ok, err := parseStructuredFinishDecision(content); ok || err != nil {
		return decision, err
	}
	return agent.Decision{
		Finish: &agent.FinishAction{Value: parseFinishValue(content)},
	}, nil
}

func parseLegacyDecision(content string, allowedTools []agent.ToolDefinition) (agent.Decision, bool, error) {
	var raw decisionResponse
	decoder := json.NewDecoder(strings.NewReader(content))
	if err := decoder.Decode(&raw); err != nil {
		return agent.Decision{}, false, nil
	}
	if strings.TrimSpace(raw.Type) == "" {
		return agent.Decision{}, false, nil
	}
	decision := agent.Decision{
		Thought: strings.TrimSpace(raw.Thought),
	}
	switch strings.TrimSpace(raw.Type) {
	case "shell":
		shell := strings.TrimSpace(raw.Shell)
		if shell == "" {
			return agent.Decision{}, true, fmt.Errorf(`anthropic response type "shell" must include non-empty "shell"`)
		}
		decision.Shell = &agent.ShellAction{Source: shell}
	case "tool":
		toolAction, err := parseLegacyToolAction(raw, allowedTools)
		if err != nil {
			return agent.Decision{}, true, err
		}
		decision.Tool = toolAction
	case "finish":
		var value any
		if len(raw.Result) > 0 && string(raw.Result) != "null" {
			if err := json.Unmarshal(raw.Result, &value); err != nil {
				return agent.Decision{}, true, fmt.Errorf("could not decode finish result: %w", err)
			}
		}
		decision.Finish = &agent.FinishAction{Value: value}
	default:
		return agent.Decision{}, true, fmt.Errorf(`anthropic response must set "type" to "shell", "tool", or "finish"`)
	}
	return decision, true, nil
}

func parseLegacyToolAction(raw decisionResponse, allowedTools []agent.ToolDefinition) (*agent.ToolAction, error) {
	name := strings.TrimSpace(raw.Name)
	if name == "" {
		name = strings.TrimSpace(raw.Tool)
	}
	if name == "" {
		return nil, fmt.Errorf(`anthropic response type "tool" must include non-empty "name"`)
	}
	def, ok := allowedToolDefinition(allowedTools, name)
	if !ok {
		return nil, fmt.Errorf("anthropic response called unavailable tool %q", name)
	}
	if def.Kind == agent.ToolKindCustom {
		input, err := extractLegacyCustomToolInput(strings.TrimSpace(raw.Input), raw.Args)
		if err != nil {
			return nil, err
		}
		return &agent.ToolAction{
			Name:  def.Name,
			Kind:  agent.ToolKindCustom,
			Input: input,
		}, nil
	}
	input := strings.TrimSpace(raw.Input)
	if len(raw.Args) > 0 && string(raw.Args) != "null" {
		input = strings.TrimSpace(string(raw.Args))
	}
	if input == "" {
		input = "{}"
	}
	return &agent.ToolAction{
		Name:  def.Name,
		Kind:  agent.ToolKindFunction,
		Input: input,
	}, nil
}

func parseBoundaryToolDecision(content string, allowedTools []agent.ToolDefinition) (agent.Decision, bool, error) {
	var raw boundaryToolCallResponse
	decoder := json.NewDecoder(strings.NewReader(content))
	if err := decoder.Decode(&raw); err != nil {
		return agent.Decision{}, false, nil
	}
	if strings.TrimSpace(raw.Type) == "" {
		return agent.Decision{}, false, nil
	}
	switch strings.TrimSpace(raw.Type) {
	case "function_call":
		decision, err := parseTextFunctionCall(strings.TrimSpace(raw.Name), strings.TrimSpace(raw.Arguments), allowedTools)
		return decision, true, err
	case "custom_tool_call":
		name := strings.TrimSpace(raw.Name)
		if name == "" {
			return agent.Decision{}, true, fmt.Errorf(`custom_tool_call must include non-empty "name"`)
		}
		def, ok := allowedToolDefinition(allowedTools, name)
		if !ok {
			return agent.Decision{}, true, fmt.Errorf("anthropic response called unavailable tool %q", name)
		}
		if def.Kind != agent.ToolKindCustom {
			return agent.Decision{}, true, fmt.Errorf("anthropic response used custom_tool_call for non-custom tool %q", name)
		}
		input := strings.TrimSpace(raw.Input)
		if input == "" {
			return agent.Decision{}, true, fmt.Errorf(`custom_tool_call must include non-empty "input"`)
		}
		return agent.Decision{
			Tool: &agent.ToolAction{
				Name:  def.Name,
				Kind:  agent.ToolKindCustom,
				Input: input,
			},
		}, true, nil
	case "local_shell_call":
		input, err := parseLocalShellInput(raw.Action)
		if err != nil {
			return agent.Decision{}, true, err
		}
		return agent.Decision{
			Tool: &agent.ToolAction{
				Name:  agent.ToolNameRunShell,
				Kind:  agent.ToolKindFunction,
				Input: input,
			},
		}, true, nil
	default:
		return agent.Decision{}, false, nil
	}
}

func parseTextFunctionCall(name, arguments string, allowedTools []agent.ToolDefinition) (agent.Decision, error) {
	if name == "" {
		return agent.Decision{}, fmt.Errorf(`function_call must include non-empty "name"`)
	}
	def, ok := allowedToolDefinition(allowedTools, name)
	if !ok {
		return agent.Decision{}, fmt.Errorf("anthropic response called unavailable tool %q", name)
	}
	if def.Kind == agent.ToolKindCustom {
		input, err := extractCustomToolInputString(arguments)
		if err != nil {
			return agent.Decision{}, err
		}
		return agent.Decision{
			Tool: &agent.ToolAction{
				Name:  def.Name,
				Kind:  agent.ToolKindCustom,
				Input: input,
			},
		}, nil
	}
	input := strings.TrimSpace(arguments)
	if input == "" {
		input = "{}"
	}
	return agent.Decision{
		Tool: &agent.ToolAction{
			Name:  def.Name,
			Kind:  agent.ToolKindFunction,
			Input: input,
		},
	}, nil
}

func parseToolUseDecision(block anthropicContentBlock, allowedTools []agent.ToolDefinition) (agent.Decision, error) {
	if strings.TrimSpace(block.Type) != "tool_use" {
		return agent.Decision{}, fmt.Errorf("unsupported anthropic content block type %q", block.Type)
	}
	name := strings.TrimSpace(block.Name)
	if name == "" {
		return agent.Decision{}, fmt.Errorf("anthropic tool_use block must include non-empty name")
	}
	def, ok := allowedToolDefinition(allowedTools, name)
	if !ok {
		return agent.Decision{}, fmt.Errorf("anthropic response called unavailable tool %q", name)
	}
	if def.Kind == agent.ToolKindCustom {
		input, err := extractCustomToolInput(block.Input)
		if err != nil {
			return agent.Decision{}, err
		}
		return agent.Decision{
			Tool: &agent.ToolAction{
				Name:  def.Name,
				Kind:  agent.ToolKindCustom,
				Input: input,
			},
		}, nil
	}
	input, err := compactRawJSON(block.Input)
	if err != nil {
		return agent.Decision{}, err
	}
	if input == "" {
		input = "{}"
	}
	return agent.Decision{
		Tool: &agent.ToolAction{
			Name:  def.Name,
			Kind:  agent.ToolKindFunction,
			Input: input,
		},
	}, nil
}

func buildAnthropicTools(tools []agent.ToolDefinition) []anthropicTool {
	if len(tools) == 0 {
		return nil
	}
	result := make([]anthropicTool, 0, len(tools))
	for _, tool := range tools {
		result = append(result, anthropicTool{
			Name:          tool.Name,
			Description:   anthropicToolDescription(tool),
			Strict:        tool.Strict,
			InputSchema:   anthropicToolSchema(tool),
			InputExamples: anthropicToolInputExamples(tool),
		})
	}
	return result
}

func anthropicToolDescription(tool agent.ToolDefinition) string {
	description := strings.TrimSpace(tool.Description)
	if tool.CustomFormat == nil {
		return description
	}
	if format := anthropicToolFormatLabel(tool.CustomFormat); format != "" {
		if description == "" {
			return "Input format: " + format + "."
		}
		return description + "\n\nInput format: " + format + "."
	}
	return description
}

func anthropicToolFormatLabel(format *agent.ToolFormat) string {
	if format == nil {
		return ""
	}
	typeName := strings.TrimSpace(format.Type)
	syntax := strings.TrimSpace(format.Syntax)
	switch {
	case typeName != "" && syntax != "":
		return typeName + "/" + syntax
	case typeName != "":
		return typeName
	case syntax != "":
		return syntax
	default:
		return ""
	}
}

func firstAnthropicToolExample(examples []string) string {
	for _, example := range examples {
		example = strings.TrimSpace(example)
		if example != "" {
			return example
		}
	}
	return ""
}

func anthropicToolInputExamples(tool agent.ToolDefinition) []map[string]any {
	if len(tool.Examples) == 0 {
		return nil
	}
	examples := make([]map[string]any, 0, len(tool.Examples))
	for _, example := range tool.Examples {
		if payload, ok := anthropicToolInputExample(tool, example); ok {
			examples = append(examples, payload)
		}
	}
	if len(examples) == 0 {
		return nil
	}
	return examples
}

func anthropicToolInputExample(tool agent.ToolDefinition, example string) (map[string]any, bool) {
	example = strings.TrimSpace(example)
	if example == "" {
		return nil, false
	}
	if tool.Kind == agent.ToolKindCustom {
		return map[string]any{anthropicCustomToolInputField(tool): example}, true
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(example), &payload); err != nil || payload == nil {
		return nil, false
	}
	return payload, true
}

func anthropicToolSchema(tool agent.ToolDefinition) map[string]any {
	if tool.Kind == agent.ToolKindCustom {
		if tool.Name == agent.ToolNameApplyPatch {
			return map[string]any{
				"type": "object",
				"properties": map[string]any{
					"patch": map[string]any{
						"type":        "string",
						"description": "Structured patch text. Provide the full patch body including \"*** Begin Patch\" and \"*** End Patch\".",
					},
				},
				"required":             []string{"patch"},
				"additionalProperties": false,
			}
		}
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				anthropicCustomToolInputField(tool): map[string]any{
					"type":        "string",
					"description": "Freeform custom tool input.",
				},
			},
			"required":             []string{anthropicCustomToolInputField(tool)},
			"additionalProperties": false,
		}
	}
	if len(tool.Parameters) != 0 {
		return tool.Parameters
	}
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"required":             []string{},
		"additionalProperties": false,
	}
}

func anthropicCustomToolInputField(tool agent.ToolDefinition) string {
	if tool.Name == agent.ToolNameApplyPatch {
		return "patch"
	}
	return "input"
}

func extractAnthropicText(blocks []anthropicContentBlock) string {
	var b strings.Builder
	for _, block := range blocks {
		if block.Type != "text" || strings.TrimSpace(block.Text) == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(block.Text)
	}
	return b.String()
}

func parseStructuredFinishDecision(content string) (agent.Decision, bool, error) {
	content = unwrapCodeFence(strings.TrimSpace(content))
	if content == "" {
		return agent.Decision{}, false, nil
	}

	var raw structuredFinishResponse
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return agent.Decision{}, false, nil
	}
	if strings.TrimSpace(raw.Type) == "" {
		return agent.Decision{}, false, nil
	}
	if strings.TrimSpace(raw.Type) != "finish" {
		return agent.Decision{}, true, fmt.Errorf(`anthropic structured finish must set "type" to "finish"`)
	}

	var value any
	if len(raw.Result) > 0 && string(raw.Result) != "null" {
		if err := json.Unmarshal(raw.Result, &value); err != nil {
			return agent.Decision{}, true, fmt.Errorf("could not decode finish result: %w", err)
		}
	}
	return agent.Decision{
		Thought: strings.TrimSpace(raw.Thought),
		Finish:  &agent.FinishAction{Value: value},
	}, true, nil
}

func compactRawJSON(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("could not decode anthropic tool input: %w", err)
	}
	return compactJSON(value), nil
}

func extractCustomToolInput(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "", fmt.Errorf("custom tool input must not be empty")
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", fmt.Errorf("could not decode custom tool input: %w", err)
	}
	return extractCustomToolInputPayload(payload)
}

func extractCustomToolInputString(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("custom tool input must not be empty")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return "", fmt.Errorf("could not decode custom tool input: %w", err)
	}
	return extractCustomToolInputPayload(payload)
}

func extractCustomToolInputPayload(payload map[string]any) (string, error) {
	for _, key := range []string{"patch", "input"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return value, nil
		}
	}
	return "", fmt.Errorf("custom tool input must include non-empty string field \"patch\" or \"input\"")
}

func extractLegacyCustomToolInput(input string, args json.RawMessage) (string, error) {
	input = strings.TrimSpace(input)
	if input != "" {
		return input, nil
	}
	args = bytes.TrimSpace(args)
	if len(args) == 0 || string(args) == "null" {
		return "", fmt.Errorf(`anthropic response type "tool" must include non-empty "input" or wrapped "args" for custom tools`)
	}
	if payload, err := extractCustomToolInput(args); err == nil {
		return payload, nil
	}
	var direct string
	if err := json.Unmarshal(args, &direct); err == nil && strings.TrimSpace(direct) != "" {
		return strings.TrimSpace(direct), nil
	}
	return "", fmt.Errorf(`anthropic response type "tool" must include non-empty "input" or wrapped "args" for custom tools`)
}

func parseLocalShellInput(action *boundaryLocalShellRun) (string, error) {
	if action == nil {
		return "", fmt.Errorf("local_shell_call must include action")
	}
	if strings.TrimSpace(action.Type) != "" && strings.TrimSpace(action.Type) != "exec" {
		return "", fmt.Errorf(`local_shell_call action.type must be "exec"`)
	}
	command, err := extractShellCommand(action.Command)
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"command": command,
	}
	if strings.TrimSpace(action.WorkingDirectory) != "" {
		payload["workdir"] = strings.TrimSpace(action.WorkingDirectory)
	}
	if action.TimeoutMS > 0 {
		payload["timeout_ms"] = action.TimeoutMS
	}
	return compactJSON(payload), nil
}

func extractShellCommand(command []string) (string, error) {
	switch {
	case len(command) == 0:
		return "", fmt.Errorf("local_shell_call action.command must not be empty")
	case len(command) == 1:
		return strings.TrimSpace(command[0]), nil
	case len(command) == 3 && (command[0] == "bash" || command[0] == "sh" || command[0] == "/bin/bash" || command[0] == "/bin/sh") && command[1] == "-lc":
		return strings.TrimSpace(command[2]), nil
	default:
		return "", fmt.Errorf("local_shell_call action.command must be a single command string or a [bash, -lc, snippet] style exec")
	}
}

func allowedToolDefinition(allowedTools []agent.ToolDefinition, name string) (agent.ToolDefinition, bool) {
	for _, tool := range allowedTools {
		if tool.Name == name {
			return tool, true
		}
	}
	return agent.ToolDefinition{}, false
}

func compactJSON(value any) string {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "{}"
	}
	return strings.TrimSpace(b.String())
}

func parseFinishValue(content string) any {
	var value any
	if err := json.Unmarshal([]byte(content), &value); err == nil {
		return value
	}
	return content
}

func unwrapCodeFence(content string) string {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "```") {
		return content
	}
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	return strings.TrimSpace(content)
}

func parseModelAndReasoningLevel(model string) (string, string) {
	model = strings.TrimSpace(model)
	lastDash := strings.LastIndex(model, "-")
	if lastDash <= 0 || lastDash == len(model)-1 {
		return model, ""
	}
	base := strings.TrimSpace(model[:lastDash])
	level := strings.TrimSpace(model[lastDash+1:])
	if base == "" {
		return model, ""
	}
	switch level {
	case "low", "medium", "high", "max":
		return base, level
	default:
		return model, ""
	}
}
