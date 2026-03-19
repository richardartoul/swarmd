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
)

// DefaultSystemPrompt is kept for backward compatibility.
var DefaultSystemPrompt = agent.DefaultSystemPrompt

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
}

// Driver implements [agent.Driver] using the OpenAI Responses API.
type Driver struct {
	apiKey               string
	baseURL              string
	model                string
	reasoningEffort      string
	client               *http.Client
	promptCacheKey       string
	promptCacheRetention string
}

type responsesRequest struct {
	Model                string               `json:"model"`
	Instructions         string               `json:"instructions,omitempty"`
	Reasoning            *responsesReasoning  `json:"reasoning,omitempty"`
	Input                []responsesInputItem `json:"input"`
	Text                 *responsesTextConfig `json:"text,omitempty"`
	Tools                []responsesTool      `json:"tools,omitempty"`
	ToolChoice           string               `json:"tool_choice,omitempty"`
	ParallelToolCalls    *bool                `json:"parallel_tool_calls,omitempty"`
	PromptCacheKey       string               `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention string               `json:"prompt_cache_retention,omitempty"`
}

type responsesReasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type responsesTextConfig struct {
	Format *responsesTextFormat `json:"format,omitempty"`
}

type responsesTextFormat struct {
	Type        string         `json:"type"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Schema      map[string]any `json:"schema,omitempty"`
	Strict      bool           `json:"strict,omitempty"`
}

type responsesInputItem struct {
	Type      string `json:"type,omitempty"`
	Role      string `json:"role,omitempty"`
	Content   string `json:"content,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Input     string `json:"input,omitempty"`
	Output    string `json:"output,omitempty"`
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
	Summary   []responsesOutputContent `json:"summary"`
}

type responsesOutputContent struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
}

type apiErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

type openAIFunctionCall struct {
	Name      string
	Arguments string
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
	if !supportsResponsesStructuredTextFormat(model) {
		return nil, fmt.Errorf("openai model %q must support structured outputs", model)
	}
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

	return d.nextResponses(ctx, req, d.adapterCapabilities())
}

func (d *Driver) nextResponses(ctx context.Context, req agent.Request, caps openAIAdapterCapabilities) (agent.Decision, error) {
	requestBody := d.buildResponsesRequest(req, caps)
	return d.completeResponses(ctx, requestBody, req.Tools, caps)
}

func (d *Driver) buildResponsesRequest(req agent.Request, caps openAIAdapterCapabilities) responsesRequest {
	requestBody := responsesRequest{
		Model:                d.model,
		Instructions:         buildResponsesInstructions(req.Messages),
		Input:                buildResponsesInput(req, caps),
		Text:                 d.responsesTextConfig(req),
		PromptCacheKey:       d.promptCacheKey,
		PromptCacheRetention: responsesPromptCacheRetention(d.promptCacheRetention),
	}
	if reasoning := responsesReasoningConfig(d.model, d.reasoningEffort); reasoning != nil {
		requestBody.Reasoning = reasoning
	}
	if len(req.Tools) > 0 {
		requestBody.Tools = buildResponsesTools(req.Tools, caps)
		requestBody.ToolChoice = "auto"
		requestBody.ParallelToolCalls = boolPtr(false)
	}
	return requestBody
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
	agent.MaybeWriteDebugPrompt(body.Bytes())

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

func parseResponsesDecision(response responsesResponse, allowedTools []agent.ToolDefinition, caps openAIAdapterCapabilities) (agent.Decision, error) {
	if refusal := extractResponsesRefusal(response.Output); refusal != "" {
		return agent.Decision{}, fmt.Errorf("openai response refused request: %s", refusal)
	}
	finalText := strings.TrimSpace(response.OutputText)
	if finalText == "" {
		finalText = strings.TrimSpace(extractResponsesOutputText(response.Output))
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
		thought := extractResponsesToolThought(response.Output)
		if thought == "" {
			thought = finalText
		}
		if thought != "" {
			decision.Thought = thought
		}
		return decision, nil
	}
	text := finalText
	if text == "" {
		return agent.Decision{}, fmt.Errorf("openai responses output did not include text or tool calls")
	}
	return parseStrictFinalDecision(text)
}

func parseStrictFinalDecision(content string) (agent.Decision, error) {
	thought, value, err := agent.ParseStrictFinalResponse(unwrapCodeFence(strings.TrimSpace(content)))
	if err != nil {
		return agent.Decision{}, err
	}
	return agent.Decision{
		Thought: thought,
		Finish:  &agent.FinishAction{Value: value},
	}, nil
}

func parseToolCallDecision(call openAIFunctionCall, allowedTools []agent.ToolDefinition, caps openAIAdapterCapabilities) (agent.Decision, error) {
	adapters := buildOpenAIToolAdapters(allowedTools, caps)
	allowedByName := make(map[string]openAIToolAdapter, len(adapters))
	for _, adapter := range adapters {
		allowedByName[adapter.ExposedName] = adapter
	}
	name := strings.TrimSpace(call.Name)
	adapter, ok := allowedByName[name]
	if !ok {
		return agent.Decision{}, fmt.Errorf("openai response called unavailable tool %q", name)
	}
	def := adapter.Tool

	if def.Kind == agent.ToolKindCustom {
		patch, err := extractCustomToolInput(call.Arguments)
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

	input := strings.TrimSpace(call.Arguments)
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
		return parseToolCallDecision(openAIFunctionCall{
			Name:      item.Name,
			Arguments: item.Arguments,
		}, allowedTools, caps)
	case "custom_tool_call":
		name := strings.TrimSpace(item.Name)
		if name == "" {
			return agent.Decision{}, fmt.Errorf("custom_tool_call must include non-empty name")
		}
		adapter, ok := allowedOpenAIToolAdapter(allowedTools, caps, name)
		if !ok {
			return agent.Decision{}, fmt.Errorf("openai response called unavailable tool %q", name)
		}
		if adapter.Tool.Kind != agent.ToolKindCustom {
			return agent.Decision{}, fmt.Errorf("openai response used custom_tool_call for non-custom tool %q", name)
		}
		input := strings.TrimSpace(item.Input)
		if input == "" {
			return agent.Decision{}, fmt.Errorf("custom_tool_call must include non-empty input")
		}
		return agent.Decision{
			Tool: &agent.ToolAction{
				Name:  adapter.Tool.Name,
				Kind:  agent.ToolKindCustom,
				Input: input,
			},
		}, nil
	default:
		return agent.Decision{}, fmt.Errorf("unsupported responses tool call type %q", item.Type)
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
			Description: openAIToolDescription(tool),
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

func buildResponsesInstructions(messages []agent.Message) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		if message.Role != agent.MessageRoleSystem {
			continue
		}
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		parts = append(parts, content)
	}
	return strings.Join(parts, "\n\n")
}

func buildResponsesInput(req agent.Request, caps openAIAdapterCapabilities) []responsesInputItem {
	if len(req.Messages) == 0 {
		return nil
	}
	prefix, footer := splitRequestMessages(req.Messages)
	replays := agent.BuildStepReplays(req.Steps)
	input := make([]responsesInputItem, 0, len(req.Messages)+len(replays)*3)
	for _, message := range prefix {
		if message.Role == agent.MessageRoleSystem {
			continue
		}
		input = append(input, responsesInputItem{
			Role:    message.Role,
			Content: message.Content,
		})
	}
	adapters := openAIToolAdaptersByInternalName(req.Tools, caps)
	for _, replay := range replays {
		adapter, ok := adapters[replay.ToolName]
		input = append(input, buildResponsesReplayItems(replay, adapter, ok)...)
	}
	if footer.Role != agent.MessageRoleSystem {
		input = append(input, responsesInputItem{
			Role:    footer.Role,
			Content: footer.Content,
		})
	}
	return input
}

func splitRequestMessages(messages []agent.Message) ([]agent.Message, agent.Message) {
	if len(messages) == 0 {
		return nil, agent.Message{}
	}
	return messages[:len(messages)-1], messages[len(messages)-1]
}

func buildResponsesReplayItems(replay agent.StepReplay, adapter openAIToolAdapter, ok bool) []responsesInputItem {
	items := make([]responsesInputItem, 0, 3)
	if replay.Thought != "" {
		items = append(items, responsesInputItem{
			Role:    agent.MessageRoleAssistant,
			Content: replay.Thought,
		})
	}
	name, arguments := openAIReplayCallNameAndArguments(replay, adapter, ok)
	boundaryKind := openAIReplayBoundaryKind(replay, adapter, ok)
	switch boundaryKind {
	case agent.ToolBoundaryKindCustom:
		items = append(items,
			responsesInputItem{
				Type:   "custom_tool_call",
				CallID: replay.CallID,
				Name:   name,
				Input:  replay.Input,
			},
			responsesInputItem{
				Type:   "custom_tool_call_output",
				CallID: replay.CallID,
				Output: replay.Output,
			},
		)
	default:
		items = append(items,
			responsesInputItem{
				Type:      "function_call",
				CallID:    replay.CallID,
				Name:      name,
				Arguments: arguments,
			},
			responsesInputItem{
				Type:   "function_call_output",
				CallID: replay.CallID,
				Output: replay.Output,
			},
		)
	}
	return items
}

func openAIReplayCallNameAndArguments(replay agent.StepReplay, adapter openAIToolAdapter, ok bool) (string, string) {
	name := replay.ToolName
	tool := agent.ToolDefinition{
		Name: replay.ToolName,
		Kind: replay.ToolKind,
	}
	if ok {
		name = adapter.ExposedName
		tool = adapter.Tool
	}
	if replay.ToolKind == agent.ToolKindCustom && openAIReplayBoundaryKind(replay, adapter, ok) != agent.ToolBoundaryKindCustom {
		return name, openAICustomToolArguments(tool, replay.Input)
	}
	arguments := strings.TrimSpace(replay.Input)
	if arguments == "" && replay.ToolKind != agent.ToolKindCustom {
		arguments = "{}"
	}
	return name, arguments
}

func openAIReplayBoundaryKind(_ agent.StepReplay, adapter openAIToolAdapter, ok bool) agent.ToolBoundaryKind {
	if ok && adapter.BoundaryKind != "" {
		return adapter.BoundaryKind
	}
	return agent.ToolBoundaryKindFunction
}

func openAIToolAdaptersByInternalName(tools []agent.ToolDefinition, caps openAIAdapterCapabilities) map[string]openAIToolAdapter {
	if len(tools) == 0 {
		return nil
	}
	adapters := buildOpenAIToolAdapters(tools, caps)
	byName := make(map[string]openAIToolAdapter, len(adapters))
	for _, adapter := range adapters {
		byName[adapter.InternalName] = adapter
	}
	return byName
}

func openAICustomToolArguments(tool agent.ToolDefinition, input string) string {
	return compactJSON(map[string]any{
		openAICustomToolInputField(tool): input,
	})
}

func openAICustomToolInputField(tool agent.ToolDefinition) string {
	if tool.Name == agent.ToolNameApplyPatch {
		return "patch"
	}
	return "input"
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

func allowedOpenAIToolAdapter(allowedTools []agent.ToolDefinition, caps openAIAdapterCapabilities, exposedName string) (openAIToolAdapter, bool) {
	for _, adapter := range buildOpenAIToolAdapters(allowedTools, caps) {
		if adapter.ExposedName == exposedName {
			return adapter, true
		}
	}
	return openAIToolAdapter{}, false
}

func allowedToolDefinition(allowedTools []agent.ToolDefinition, name string) (agent.ToolDefinition, bool) {
	for _, tool := range allowedTools {
		if tool.Name == name {
			return tool, true
		}
	}
	return agent.ToolDefinition{}, false
}

func (d *Driver) adapterCapabilities() openAIAdapterCapabilities {
	return openAIAdapterCapabilities{
		SupportsCustomTools: true,
	}
}

func (d *Driver) responsesTextConfig(req agent.Request) *responsesTextConfig {
	return &responsesTextConfig{
		Format: responsesJSONSchemaFormat(
			"agent_final_response",
			"Return the final response object when no tool call is needed.",
			agent.StrictFinalResponseSchema(),
		),
	}
}

func responsesJSONSchemaFormat(name, description string, schema map[string]any) *responsesTextFormat {
	return &responsesTextFormat{
		Type:        "json_schema",
		Name:        name,
		Description: description,
		Schema:      schema,
		Strict:      true,
	}
}

func supportsResponsesStructuredTextFormat(model string) bool {
	model = strings.TrimSpace(model)
	switch {
	case strings.HasPrefix(model, "gpt-5"),
		strings.HasPrefix(model, "gpt-4.1"),
		strings.HasPrefix(model, "o1"),
		strings.HasPrefix(model, "o3"),
		strings.HasPrefix(model, "o4"):
		return true
	case model == "chatgpt-4o-latest",
		model == "gpt-4o",
		model == "gpt-4o-mini":
		return true
	case model == "gpt-4o-2024-05-13":
		return false
	case strings.HasPrefix(model, "gpt-4o-"):
		return model >= "gpt-4o-2024-08-06"
	case strings.HasPrefix(model, "gpt-4o-mini-"):
		return model >= "gpt-4o-mini-2024-07-18"
	default:
		return false
	}
}

func supportsResponsesReasoningSummary(model string) bool {
	model = strings.TrimSpace(model)
	switch {
	case strings.HasPrefix(model, "gpt-5"),
		strings.HasPrefix(model, "o1"),
		strings.HasPrefix(model, "o3"),
		strings.HasPrefix(model, "o4"):
		return true
	default:
		return false
	}
}

func openAIToolDescription(tool agent.ToolDefinition) string {
	sections := make([]string, 0, 3)
	if description := strings.TrimSpace(tool.Description); description != "" {
		sections = append(sections, description)
	}
	if notes := strings.TrimSpace(tool.OutputNotes); notes != "" {
		sections = append(sections, "Output notes: "+notes)
	}
	if tags := strings.TrimSpace(strings.Join(tool.SafetyTags, ", ")); tags != "" {
		sections = append(sections, "Safety tags: "+tags+".")
	}
	return strings.Join(sections, "\n\n")
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

func responsesReasoningConfig(model, effort string) *responsesReasoning {
	config := &responsesReasoning{}
	if strings.TrimSpace(effort) != "" {
		config.Effort = strings.TrimSpace(effort)
	}
	if supportsResponsesReasoningSummary(model) {
		config.Summary = "auto"
	}
	if config.Effort == "" && config.Summary == "" {
		return nil
	}
	return config
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

func extractResponsesToolThought(items []responsesOutputItem) string {
	var b strings.Builder
	appendText := func(text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(text)
	}
	for _, item := range items {
		switch item.Type {
		case "function_call", "custom_tool_call":
			return b.String()
		case "reasoning":
			appendText(extractResponsesReasoningSummaryText(item))
		case "message":
			appendText(extractResponsesMessageText(item))
		}
	}
	return b.String()
}

func extractResponsesReasoningSummaryText(item responsesOutputItem) string {
	if text := extractResponsesContentText(item.Summary, "summary_text", "text"); text != "" {
		return text
	}
	return extractResponsesContentText(item.Content, "reasoning_text", "text")
}

func extractResponsesMessageText(item responsesOutputItem) string {
	return extractResponsesContentText(item.Content, "output_text", "text")
}

func extractResponsesContentText(content []responsesOutputContent, allowedTypes ...string) string {
	if len(content) == 0 {
		return ""
	}
	allowed := make(map[string]struct{}, len(allowedTypes))
	for _, value := range allowedTypes {
		allowed[value] = struct{}{}
	}
	var b strings.Builder
	for _, block := range content {
		if _, ok := allowed[block.Type]; !ok || strings.TrimSpace(block.Text) == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(block.Text)
	}
	return b.String()
}

func extractResponsesRefusal(items []responsesOutputItem) string {
	for _, item := range items {
		if item.Type != "message" {
			continue
		}
		for _, content := range item.Content {
			if content.Type != "refusal" {
				continue
			}
			if strings.TrimSpace(content.Refusal) != "" {
				return strings.TrimSpace(content.Refusal)
			}
			if strings.TrimSpace(content.Text) != "" {
				return strings.TrimSpace(content.Text)
			}
		}
	}
	return ""
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
