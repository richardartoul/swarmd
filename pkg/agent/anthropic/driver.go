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
	Content string `json:"content"`
}

type anthropicThinking struct {
	Type string `json:"type"`
}

type anthropicOutputConfig struct {
	Effort string `json:"effort,omitempty"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
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
		Messages:  make([]anthropicMessage, 0, len(req.Messages)),
	}
	if d.reasoning != "" {
		payload.Thinking = &anthropicThinking{Type: "adaptive"}
		payload.OutputConfig = &anthropicOutputConfig{Effort: d.reasoning}
	}
	var systemParts []string
	for _, message := range req.Messages {
		switch message.Role {
		case agent.MessageRoleSystem:
			if strings.TrimSpace(message.Content) != "" {
				systemParts = append(systemParts, message.Content)
			}
		case agent.MessageRoleUser, agent.MessageRoleAssistant:
			payload.Messages = append(payload.Messages, anthropicMessage{
				Role:    message.Role,
				Content: message.Content,
			})
		default:
			return messagesRequest{}, fmt.Errorf("unsupported anthropic message role %q", message.Role)
		}
	}
	if len(systemParts) > 0 {
		payload.System = strings.Join(systemParts, "\n\n")
	}
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
	if decision, ok, err := parseStructuredFinishDecision(text); ok || err != nil {
		return decision, err
	}
	if text != "" {
		return agent.Decision{
			Finish: &agent.FinishAction{Value: parseFinishValue(text)},
		}, nil
	}
	stopReason := strings.TrimSpace(response.StopReason)
	if stopReason == "" {
		return agent.Decision{}, fmt.Errorf("anthropic response content was empty")
	}
	return agent.Decision{}, fmt.Errorf("anthropic response with stop_reason %q did not include text or tool use", stopReason)
}

func parseToolUseDecision(block anthropicContentBlock, allowedTools []agent.ToolDefinition) (agent.Decision, error) {
	if strings.TrimSpace(block.Type) != "tool_use" {
		return agent.Decision{}, fmt.Errorf("unsupported anthropic content block type %q", block.Type)
	}
	name := strings.TrimSpace(block.Name)
	if name == "" {
		return agent.Decision{}, fmt.Errorf("anthropic tool_use block must include non-empty name")
	}
	allowedByName := make(map[string]agent.ToolDefinition, len(allowedTools))
	for _, tool := range allowedTools {
		allowedByName[tool.Name] = tool
	}
	def, ok := allowedByName[name]
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
			Name:        tool.Name,
			Description: anthropicToolDescription(tool),
			InputSchema: anthropicToolSchema(tool),
		})
	}
	return result
}

func anthropicToolDescription(tool agent.ToolDefinition) string {
	description := strings.TrimSpace(tool.Description)
	if tool.CustomFormat == nil {
		return description
	}
	var parts []string
	if description != "" {
		parts = append(parts, description)
	}
	if format := anthropicToolFormatLabel(tool.CustomFormat); format != "" {
		parts = append(parts, "Input format: "+format+".")
	}
	if definition := strings.TrimSpace(tool.CustomFormat.Definition); definition != "" {
		parts = append(parts, "Input definition:\n"+definition)
	}
	if example := firstAnthropicToolExample(tool.Examples); example != "" {
		parts = append(parts, "Example:\n"+example)
	}
	return strings.Join(parts, "\n\n")
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
				"input": map[string]any{
					"type":        "string",
					"description": "Freeform custom tool input.",
				},
			},
			"required":             []string{"input"},
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
	for _, key := range []string{"patch", "input"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return value, nil
		}
	}
	return "", fmt.Errorf("custom tool input must include non-empty string field \"patch\" or \"input\"")
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
