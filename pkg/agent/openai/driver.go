// See LICENSE for licensing information

// Package openai provides an OpenAI-backed implementation of [agent.Driver].
package openai

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
	// DefaultBaseURL is the default OpenAI API base URL.
	DefaultBaseURL = "https://api.openai.com/v1"

	// DefaultSystemPrompt is kept for backward compatibility.
	DefaultSystemPrompt = agent.DefaultSystemPrompt
)

// Config configures a new OpenAI-backed driver.
type Config struct {
	APIKey               string
	BaseURL              string
	Model                string
	HTTPClient           *http.Client
	PromptCacheKey       string
	PromptCacheRetention string

	// Deprecated: configure the prompt on [agent.Config].
	SystemPrompt string

	// Deprecated: configure history preservation on [agent.Config].
	PreserveConversation bool
}

// Driver implements [agent.Driver] using the OpenAI chat completions API.
type Driver struct {
	apiKey               string
	baseURL              string
	model                string
	reasoningEffort      string
	client               *http.Client
	promptCacheKey       string
	promptCacheRetention string
}

type chatCompletionRequest struct {
	Model                string               `json:"model"`
	ReasoningEffort      string               `json:"reasoning_effort,omitempty"`
	Messages             []chatMessage        `json:"messages"`
	Tools                []chatCompletionTool `json:"tools,omitempty"`
	ToolChoice           string               `json:"tool_choice,omitempty"`
	ParallelToolCalls    *bool                `json:"parallel_tool_calls,omitempty"`
	PromptCacheKey       string               `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention string               `json:"prompt_cache_retention,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionTool struct {
	Type     string                     `json:"type"`
	Function chatCompletionFunctionTool `json:"function"`
}

type chatCompletionFunctionTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Strict      bool           `json:"strict,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type chatCompletionChoice struct {
	Message chatCompletionMessage `json:"message"`
}

type chatCompletionMessage struct {
	Content   string                   `json:"content"`
	ToolCalls []chatCompletionToolCall `json:"tool_calls,omitempty"`
}

type chatCompletionToolCall struct {
	ID       string                     `json:"id"`
	Type     string                     `json:"type"`
	Function chatCompletionFunctionCall `json:"function"`
}

type chatCompletionFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatCompletionUsage struct {
	PromptTokensDetails chatPromptTokensDetails `json:"prompt_tokens_details"`
}

type chatPromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type chatCompletionResponse struct {
	Choices []chatCompletionChoice `json:"choices"`
	Usage   chatCompletionUsage    `json:"usage"`
}

type responsesRequest struct {
	Model                string              `json:"model"`
	Reasoning            *responsesReasoning `json:"reasoning,omitempty"`
	Input                []chatMessage       `json:"input"`
	Tools                []responsesTool     `json:"tools,omitempty"`
	ToolChoice           string              `json:"tool_choice,omitempty"`
	ParallelToolCalls    *bool               `json:"parallel_tool_calls,omitempty"`
	PromptCacheKey       string              `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention string              `json:"prompt_cache_retention,omitempty"`
}

type responsesReasoning struct {
	Effort string `json:"effort,omitempty"`
}

type responsesTool struct {
	Type        string           `json:"type"`
	Name        string           `json:"name,omitempty"`
	Description string           `json:"description,omitempty"`
	Strict      bool             `json:"strict,omitempty"`
	Parameters  map[string]any   `json:"parameters,omitempty"`
	Format      *responsesFormat `json:"format,omitempty"`
}

type responsesFormat struct {
	Type       string `json:"type,omitempty"`
	Syntax     string `json:"syntax,omitempty"`
	Definition string `json:"definition,omitempty"`
}

type responsesUsage struct {
	InputTokensDetails responsesInputTokensDetails `json:"input_tokens_details"`
}

type responsesInputTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type responsesResponse struct {
	Output     []responsesOutputItem `json:"output"`
	OutputText string                `json:"output_text"`
	Usage      responsesUsage        `json:"usage"`
}

type responsesOutputItem struct {
	Type      string                   `json:"type"`
	CallID    string                   `json:"call_id"`
	Name      string                   `json:"name"`
	Arguments string                   `json:"arguments"`
	Input     string                   `json:"input"`
	Role      string                   `json:"role"`
	Content   []responsesOutputContent `json:"content"`
}

type responsesOutputContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type apiErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
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

type openAIAdapterCapabilities struct {
	SupportsCustomTools     bool
	SupportsLocalShell      bool
	SupportsHostedWebSearch bool
}

type openAIToolAdapter struct {
	InternalName string
	ExposedName  string
	BoundaryKind agent.ToolBoundaryKind
	Tool         agent.ToolDefinition
}

// New constructs an OpenAI-backed driver.
func New(cfg Config) (*Driver, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("openai api key must not be empty")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, fmt.Errorf("openai model must not be empty")
	}
	if strings.TrimSpace(cfg.SystemPrompt) != "" {
		return nil, fmt.Errorf("openai system prompt is configured on agent.Config, not openai.Config")
	}
	if cfg.PreserveConversation {
		return nil, fmt.Errorf("openai conversation history is configured on agent.Config, not openai.Config")
	}
	promptCacheRetention := strings.TrimSpace(cfg.PromptCacheRetention)
	switch promptCacheRetention {
	case "", "in_memory", "in-memory", "24h":
	default:
		return nil, fmt.Errorf("openai prompt cache retention must be empty, in_memory, in-memory, or 24h")
	}

	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	model, reasoningEffort := parseModelAndReasoningEffort(cfg.Model)
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}

	return &Driver{
		apiKey:               cfg.APIKey,
		baseURL:              baseURL,
		model:                model,
		reasoningEffort:      reasoningEffort,
		client:               client,
		promptCacheKey:       strings.TrimSpace(cfg.PromptCacheKey),
		promptCacheRetention: promptCacheRetention,
	}, nil
}

// Next implements [agent.Driver].
func (d *Driver) Next(ctx context.Context, req agent.Request) (agent.Decision, error) {
	if len(req.Messages) == 0 {
		return agent.Decision{}, fmt.Errorf("openai request must include at least one message")
	}

	useResponses := d.shouldUseResponsesAPI(req)
	caps := d.adapterCapabilities(useResponses)
	if useResponses {
		return d.nextResponses(ctx, req, caps)
	}
	return d.nextChatCompletions(ctx, req, caps)
}

func (d *Driver) nextChatCompletions(ctx context.Context, req agent.Request, caps openAIAdapterCapabilities) (agent.Decision, error) {
	requestBody := chatCompletionRequest{
		Model:                d.model,
		ReasoningEffort:      d.reasoningEffort,
		Messages:             make([]chatMessage, 0, len(req.Messages)),
		PromptCacheKey:       d.promptCacheKey,
		PromptCacheRetention: d.promptCacheRetention,
	}
	for _, message := range req.Messages {
		requestBody.Messages = append(requestBody.Messages, chatMessage{
			Role:    message.Role,
			Content: message.Content,
		})
	}
	if len(req.Tools) > 0 {
		requestBody.Tools = buildChatCompletionTools(req.Tools, caps)
		requestBody.ToolChoice = "auto"
		requestBody.ParallelToolCalls = boolPtr(false)
	}

	decision, err := d.complete(ctx, requestBody, req.Tools, caps)
	if err != nil {
		return agent.Decision{}, err
	}
	return decision, nil
}

func (d *Driver) nextResponses(ctx context.Context, req agent.Request, caps openAIAdapterCapabilities) (agent.Decision, error) {
	requestBody := responsesRequest{
		Model:                d.model,
		Input:                make([]chatMessage, 0, len(req.Messages)),
		ToolChoice:           "auto",
		ParallelToolCalls:    boolPtr(false),
		PromptCacheKey:       d.promptCacheKey,
		PromptCacheRetention: responsesPromptCacheRetention(d.promptCacheRetention),
	}
	if d.reasoningEffort != "" {
		requestBody.Reasoning = &responsesReasoning{Effort: d.reasoningEffort}
	}
	for _, message := range req.Messages {
		requestBody.Input = append(requestBody.Input, chatMessage{
			Role:    message.Role,
			Content: message.Content,
		})
	}
	if len(req.Tools) > 0 {
		requestBody.Tools = buildResponsesTools(req.Tools, caps)
	}
	return d.completeResponses(ctx, requestBody, req.Tools, caps)
}

func (d *Driver) complete(ctx context.Context, payload chatCompletionRequest, allowedTools []agent.ToolDefinition, caps openAIAdapterCapabilities) (agent.Decision, error) {
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return agent.Decision{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+"/chat/completions", &body)
	if err != nil {
		return agent.Decision{}, err
	}
	req.Header.Set("Authorization", "Bearer "+d.apiKey)
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
			return agent.Decision{}, fmt.Errorf("openai api error (%s): %s", resp.Status, apiErr.Error.Message)
		}
		return agent.Decision{}, fmt.Errorf("openai api error (%s): %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	var completion chatCompletionResponse
	if err := json.Unmarshal(respBody, &completion); err != nil {
		return agent.Decision{}, err
	}
	if len(completion.Choices) == 0 {
		return agent.Decision{}, fmt.Errorf("openai response contained no choices")
	}

	decision, err := parseChoiceDecision(completion.Choices[0].Message, allowedTools, caps)
	if err != nil {
		return agent.Decision{}, err
	}
	decision.Usage = agent.Usage{
		CachedTokens: completion.Usage.PromptTokensDetails.CachedTokens,
	}
	return decision, nil
}

func (d *Driver) completeResponses(ctx context.Context, payload responsesRequest, allowedTools []agent.ToolDefinition, caps openAIAdapterCapabilities) (agent.Decision, error) {
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return agent.Decision{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+"/responses", &body)
	if err != nil {
		return agent.Decision{}, err
	}
	req.Header.Set("Authorization", "Bearer "+d.apiKey)
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
			return agent.Decision{}, fmt.Errorf("openai api error (%s): %s", resp.Status, apiErr.Error.Message)
		}
		return agent.Decision{}, fmt.Errorf("openai api error (%s): %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	var response responsesResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return agent.Decision{}, err
	}
	decision, err := parseResponsesDecision(response, allowedTools, caps)
	if err != nil {
		return agent.Decision{}, err
	}
	decision.Usage = agent.Usage{
		CachedTokens: response.Usage.InputTokensDetails.CachedTokens,
	}
	return decision, nil
}

func parseChoiceDecision(message chatCompletionMessage, allowedTools []agent.ToolDefinition, caps openAIAdapterCapabilities) (agent.Decision, error) {
	thought := strings.TrimSpace(message.Content)
	if len(message.ToolCalls) > 0 {
		if len(message.ToolCalls) != 1 {
			return agent.Decision{}, fmt.Errorf("openai response must include exactly one tool call when calling tools")
		}
		decision, err := parseToolCallDecision(message.ToolCalls[0], allowedTools, caps)
		if err != nil {
			return agent.Decision{}, err
		}
		if thought != "" {
			decision.Thought = thought
		}
		return decision, nil
	}
	content := thought
	if content == "" {
		return agent.Decision{}, fmt.Errorf("openai response content was empty")
	}
	return parseDecision(content, allowedTools, caps)
}

func parseResponsesDecision(response responsesResponse, allowedTools []agent.ToolDefinition, caps openAIAdapterCapabilities) (agent.Decision, error) {
	thought := strings.TrimSpace(response.OutputText)
	if thought == "" {
		thought = strings.TrimSpace(extractResponsesOutputText(response.Output))
	}
	var toolItems []responsesOutputItem
	for _, item := range response.Output {
		switch item.Type {
		case "function_call", "custom_tool_call":
			toolItems = append(toolItems, item)
		}
	}
	if len(toolItems) > 1 {
		return agent.Decision{}, fmt.Errorf("openai responses output must include exactly one tool call when calling tools")
	}
	if len(toolItems) == 1 {
		decision, err := parseResponsesToolCallDecision(toolItems[0], allowedTools, caps)
		if err != nil {
			return agent.Decision{}, err
		}
		if thought != "" {
			decision.Thought = thought
		}
		return decision, nil
	}
	text := thought
	if text == "" {
		return agent.Decision{}, fmt.Errorf("openai responses output did not include text or tool calls")
	}
	return parseDecision(text, allowedTools, caps)
}

func parseDecision(content string, allowedTools []agent.ToolDefinition, caps openAIAdapterCapabilities) (agent.Decision, error) {
	content = unwrapCodeFence(strings.TrimSpace(content))
	if content == "" {
		return agent.Decision{}, fmt.Errorf("openai response content was empty")
	}

	if decision, ok, err := parseBoundaryToolDecision(content, allowedTools, caps); ok || err != nil {
		return decision, err
	}

	if decision, ok, err := parseLegacyDecision(content); ok || err != nil {
		return decision, err
	}

	value := parseFinishValue(content)
	return agent.Decision{
		Finish: &agent.FinishAction{Value: value},
	}, nil
}

func parseLegacyDecision(content string) (agent.Decision, bool, error) {
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
			return agent.Decision{}, true, fmt.Errorf(`openai response type "shell" must include non-empty "shell"`)
		}
		decision.Shell = &agent.ShellAction{Source: shell}
	case "tool":
		name := strings.TrimSpace(raw.Name)
		if name == "" {
			name = strings.TrimSpace(raw.Tool)
		}
		if name == "" {
			return agent.Decision{}, true, fmt.Errorf(`openai response type "tool" must include non-empty "name"`)
		}
		kind := agent.ToolKindFunction
		input := strings.TrimSpace(raw.Input)
		if len(raw.Args) > 0 && string(raw.Args) != "null" {
			input = strings.TrimSpace(string(raw.Args))
		}
		if name == agent.ToolNameApplyPatch {
			kind = agent.ToolKindCustom
		}
		decision.Tool = &agent.ToolAction{
			Name:  name,
			Kind:  kind,
			Input: input,
		}
	case "finish":
		var value any
		if len(raw.Result) > 0 && string(raw.Result) != "null" {
			if err := json.Unmarshal(raw.Result, &value); err != nil {
				return agent.Decision{}, true, fmt.Errorf("could not decode finish result: %w", err)
			}
		}
		decision.Finish = &agent.FinishAction{Value: value}
	default:
		return agent.Decision{}, true, fmt.Errorf(`openai response must set "type" to "shell", "tool", or "finish"`)
	}

	return decision, true, nil
}

func parseFinishValue(content string) any {
	var value any
	if err := json.Unmarshal([]byte(content), &value); err == nil {
		return value
	}
	return content
}

func parseToolCallDecision(call chatCompletionToolCall, allowedTools []agent.ToolDefinition, caps openAIAdapterCapabilities) (agent.Decision, error) {
	if call.Type != "function" {
		return agent.Decision{}, fmt.Errorf("unsupported openai tool call type %q", call.Type)
	}

	adapters := buildOpenAIToolAdapters(allowedTools, caps)
	allowedByName := make(map[string]openAIToolAdapter, len(adapters))
	for _, adapter := range adapters {
		allowedByName[adapter.ExposedName] = adapter
	}
	adapter, ok := allowedByName[call.Function.Name]
	if !ok {
		return agent.Decision{}, fmt.Errorf("openai response called unavailable tool %q", call.Function.Name)
	}
	def := adapter.Tool

	if def.Kind == agent.ToolKindCustom {
		patch, err := extractCustomToolInput(call.Function.Arguments)
		if err != nil {
			return agent.Decision{}, err
		}
		return agent.Decision{
			Tool: &agent.ToolAction{
				Name:  def.Name,
				Kind:  agent.ToolKindCustom,
				Input: patch,
			},
		}, nil
	}

	input := strings.TrimSpace(call.Function.Arguments)
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

func parseResponsesToolCallDecision(item responsesOutputItem, allowedTools []agent.ToolDefinition, caps openAIAdapterCapabilities) (agent.Decision, error) {
	switch item.Type {
	case "function_call":
		return parseToolCallDecision(chatCompletionToolCall{
			Type: "function",
			Function: chatCompletionFunctionCall{
				Name:      item.Name,
				Arguments: item.Arguments,
			},
		}, allowedTools, caps)
	case "custom_tool_call":
		name := strings.TrimSpace(item.Name)
		if name == "" {
			return agent.Decision{}, fmt.Errorf("custom_tool_call must include non-empty name")
		}
		input := strings.TrimSpace(item.Input)
		if input == "" {
			return agent.Decision{}, fmt.Errorf("custom_tool_call must include non-empty input")
		}
		return agent.Decision{
			Tool: &agent.ToolAction{
				Name:  name,
				Kind:  agent.ToolKindCustom,
				Input: input,
			},
		}, nil
	default:
		return agent.Decision{}, fmt.Errorf("unsupported responses tool call type %q", item.Type)
	}
}

func parseBoundaryToolDecision(content string, allowedTools []agent.ToolDefinition, caps openAIAdapterCapabilities) (agent.Decision, bool, error) {
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
		call := chatCompletionToolCall{
			Type: "function",
			Function: chatCompletionFunctionCall{
				Name:      strings.TrimSpace(raw.Name),
				Arguments: strings.TrimSpace(raw.Arguments),
			},
		}
		decision, err := parseToolCallDecision(call, allowedTools, caps)
		return decision, true, err
	case "custom_tool_call":
		name := strings.TrimSpace(raw.Name)
		if name == "" {
			return agent.Decision{}, true, fmt.Errorf(`custom_tool_call must include non-empty "name"`)
		}
		input := strings.TrimSpace(raw.Input)
		if input == "" {
			return agent.Decision{}, true, fmt.Errorf(`custom_tool_call must include non-empty "input"`)
		}
		return agent.Decision{
			Tool: &agent.ToolAction{
				Name:  name,
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

func parseLocalShellInput(action *boundaryLocalShellRun) (string, error) {
	if action == nil {
		return "", fmt.Errorf("local_shell_call must include action")
	}
	if strings.TrimSpace(action.Type) != "" && strings.TrimSpace(action.Type) != "exec" {
		return "", fmt.Errorf("local_shell_call action.type must be \"exec\"")
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

func extractCustomToolInput(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("custom tool arguments must not be empty")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return "", fmt.Errorf("could not decode custom tool arguments: %w", err)
	}
	for _, key := range []string{"patch", "input"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return value, nil
		}
	}
	return "", fmt.Errorf("custom tool arguments must include non-empty string field \"patch\"")
}

func buildChatCompletionTools(tools []agent.ToolDefinition, caps openAIAdapterCapabilities) []chatCompletionTool {
	if len(tools) == 0 {
		return nil
	}
	result := make([]chatCompletionTool, 0, len(tools))
	for _, adapter := range buildOpenAIToolAdapters(tools, caps) {
		tool := adapter.Tool
		parameters := tool.Parameters
		if adapter.BoundaryKind != agent.ToolBoundaryKindFunction || tool.Kind == agent.ToolKindCustom {
			parameters = map[string]any{
				"type": "object",
				"properties": map[string]any{
					"patch": map[string]any{
						"type":        "string",
						"description": "Structured patch text for the apply_patch grammar.",
					},
				},
				"required":             []string{"patch"},
				"additionalProperties": false,
			}
		}
		if len(parameters) == 0 {
			parameters = map[string]any{
				"type":                 "object",
				"properties":           map[string]any{},
				"required":             []string{},
				"additionalProperties": false,
			}
		}
		result = append(result, chatCompletionTool{
			Type: "function",
			Function: chatCompletionFunctionTool{
				Name:        adapter.ExposedName,
				Description: tool.Description,
				Strict:      tool.Strict,
				Parameters:  parameters,
			},
		})
	}
	return result
}

func buildResponsesTools(tools []agent.ToolDefinition, caps openAIAdapterCapabilities) []responsesTool {
	if len(tools) == 0 {
		return nil
	}
	result := make([]responsesTool, 0, len(tools))
	for _, adapter := range buildOpenAIToolAdapters(tools, caps) {
		tool := adapter.Tool
		responseTool := responsesTool{
			Type:        string(adapter.BoundaryKind),
			Name:        adapter.ExposedName,
			Description: tool.Description,
		}
		switch adapter.BoundaryKind {
		case agent.ToolBoundaryKindCustom:
			if tool.CustomFormat != nil {
				responseTool.Format = &responsesFormat{
					Type:       tool.CustomFormat.Type,
					Syntax:     tool.CustomFormat.Syntax,
					Definition: tool.CustomFormat.Definition,
				}
			}
		default:
			responseTool.Type = string(agent.ToolBoundaryKindFunction)
			responseTool.Strict = tool.Strict
			responseTool.Parameters = tool.Parameters
			if len(responseTool.Parameters) == 0 {
				responseTool.Parameters = map[string]any{
					"type":                 "object",
					"properties":           map[string]any{},
					"required":             []string{},
					"additionalProperties": false,
				}
			}
		}
		result = append(result, responseTool)
	}
	return result
}

func buildOpenAIToolAdapters(tools []agent.ToolDefinition, caps openAIAdapterCapabilities) []openAIToolAdapter {
	if len(tools) == 0 {
		return nil
	}
	adapters := make([]openAIToolAdapter, 0, len(tools))
	for _, tool := range tools {
		adapter := openAIToolAdapter{
			InternalName: tool.Name,
			ExposedName:  tool.Name,
			BoundaryKind: agent.ToolBoundaryKindFunction,
			Tool:         tool,
		}
		preferredKind := tool.Interop.OpenAIPreferredKind
		fallbackKind := tool.Interop.OpenAIFallbackKind
		if fallbackKind == "" {
			fallbackKind = agent.ToolBoundaryKindFunction
		}
		fallbackName := strings.TrimSpace(tool.Interop.OpenAIFallbackToolName)
		if fallbackName == "" {
			fallbackName = tool.Name
		}
		switch preferredKind {
		case agent.ToolBoundaryKindCustom:
			if caps.SupportsCustomTools {
				adapter.BoundaryKind = preferredKind
			} else {
				adapter.BoundaryKind = fallbackKind
				adapter.ExposedName = fallbackName
			}
		case agent.ToolBoundaryKindLocalShell:
			if caps.SupportsLocalShell {
				adapter.BoundaryKind = preferredKind
			} else {
				adapter.BoundaryKind = fallbackKind
				adapter.ExposedName = fallbackName
			}
		case agent.ToolBoundaryKindWebSearch:
			if caps.SupportsHostedWebSearch {
				adapter.BoundaryKind = preferredKind
			} else {
				adapter.BoundaryKind = fallbackKind
				adapter.ExposedName = fallbackName
			}
		default:
			if preferredKind != "" {
				adapter.BoundaryKind = preferredKind
			}
		}
		adapters = append(adapters, adapter)
	}
	return adapters
}

func (d *Driver) shouldUseResponsesAPI(req agent.Request) bool {
	return len(req.Tools) > 0
}

func (d *Driver) adapterCapabilities(useResponses bool) openAIAdapterCapabilities {
	if useResponses {
		return openAIAdapterCapabilities{
			SupportsCustomTools: true,
		}
	}
	return openAIAdapterCapabilities{}
}

func boolPtr(value bool) *bool {
	return &value
}

func responsesPromptCacheRetention(value string) string {
	switch strings.TrimSpace(value) {
	case "in_memory":
		return "in-memory"
	default:
		return strings.TrimSpace(value)
	}
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

func extractResponsesOutputText(items []responsesOutputItem) string {
	var b strings.Builder
	for _, item := range items {
		if item.Type != "message" {
			continue
		}
		for _, content := range item.Content {
			switch content.Type {
			case "output_text", "text":
				if strings.TrimSpace(content.Text) == "" {
					continue
				}
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(content.Text)
			}
		}
	}
	return b.String()
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

func parseModelAndReasoningEffort(model string) (string, string) {
	model = strings.TrimSpace(model)
	for _, effort := range []string{"xhigh", "high", "medium", "minimal", "low", "none"} {
		suffix := "-x" + effort
		if base, ok := strings.CutSuffix(model, suffix); ok && strings.TrimSpace(base) != "" {
			return base, effort
		}
	}
	return model, ""
}
