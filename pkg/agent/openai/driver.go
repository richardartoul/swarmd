// See LICENSE for licensing information

// Package openai provides an OpenAI-backed implementation of [agent.Driver].
package openai

import (
	"bytes"
	"context"
	"encoding/base64"
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
	PreviousResponseID   string               `json:"previous_response_id,omitempty"`
	Reasoning            *responsesReasoning  `json:"reasoning,omitempty"`
	Input                []responsesInputItem `json:"input"`
	Text                 *responsesTextConfig `json:"text,omitempty"`
	Tools                []responsesTool      `json:"tools,omitempty"`
	ToolChoice           string               `json:"tool_choice,omitempty"`
	ParallelToolCalls    *bool                `json:"parallel_tool_calls,omitempty"`
	PromptCacheKey       string               `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention string               `json:"prompt_cache_retention,omitempty"`
}

type responsesImageRequest struct {
	Model string                    `json:"model"`
	Input []responsesImageInputItem `json:"input"`
}

type responsesImageInputItem struct {
	Role    string                    `json:"role"`
	Content []responsesImageInputPart `json:"content"`
}

type responsesImageInputPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	Detail   string `json:"detail,omitempty"`
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
	Type          string                   `json:"type,omitempty"`
	Role          string                   `json:"role,omitempty"`
	Phase         string                   `json:"phase,omitempty"`
	Content       string                   `json:"-"`
	ContentBlocks []responsesOutputContent `json:"-"`
	CallID        string                   `json:"call_id,omitempty"`
	Name          string                   `json:"name,omitempty"`
	Arguments     string                   `json:"arguments,omitempty"`
	Input         string                   `json:"input,omitempty"`
	Output        string                   `json:"output,omitempty"`
	Summary       []responsesOutputContent `json:"summary,omitempty"`
}

func (i responsesInputItem) MarshalJSON() ([]byte, error) {
	type rawInput struct {
		Type      string                   `json:"type,omitempty"`
		Role      string                   `json:"role,omitempty"`
		Phase     string                   `json:"phase,omitempty"`
		Content   any                      `json:"content,omitempty"`
		CallID    string                   `json:"call_id,omitempty"`
		Name      string                   `json:"name,omitempty"`
		Arguments string                   `json:"arguments,omitempty"`
		Input     string                   `json:"input,omitempty"`
		Output    string                   `json:"output,omitempty"`
		Summary   []responsesOutputContent `json:"summary,omitempty"`
	}
	payload := rawInput{
		Type:      i.Type,
		Role:      i.Role,
		Phase:     i.Phase,
		CallID:    i.CallID,
		Name:      i.Name,
		Arguments: i.Arguments,
		Input:     i.Input,
		Output:    i.Output,
		Summary:   i.Summary,
	}
	switch {
	case len(i.ContentBlocks) > 0:
		payload.Content = i.ContentBlocks
	case i.Content != "":
		payload.Content = i.Content
	}
	return json.Marshal(payload)
}

func (i *responsesInputItem) UnmarshalJSON(data []byte) error {
	type rawInput struct {
		Type      string                   `json:"type,omitempty"`
		Role      string                   `json:"role,omitempty"`
		Phase     string                   `json:"phase,omitempty"`
		Content   json.RawMessage          `json:"content,omitempty"`
		CallID    string                   `json:"call_id,omitempty"`
		Name      string                   `json:"name,omitempty"`
		Arguments string                   `json:"arguments,omitempty"`
		Input     string                   `json:"input,omitempty"`
		Output    string                   `json:"output,omitempty"`
		Summary   []responsesOutputContent `json:"summary,omitempty"`
	}
	var decoded rawInput
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*i = responsesInputItem{
		Type:      decoded.Type,
		Role:      decoded.Role,
		Phase:     decoded.Phase,
		CallID:    decoded.CallID,
		Name:      decoded.Name,
		Arguments: decoded.Arguments,
		Input:     decoded.Input,
		Output:    decoded.Output,
		Summary:   decoded.Summary,
	}
	content := bytes.TrimSpace(decoded.Content)
	if len(content) == 0 || string(content) == "null" {
		return nil
	}
	if len(content) > 0 && content[0] == '"' {
		return json.Unmarshal(content, &i.Content)
	}
	return json.Unmarshal(content, &i.ContentBlocks)
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
	ID         string                `json:"id"`
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
	Phase     string                   `json:"phase"`
	Content   []responsesOutputContent `json:"content"`
	Summary   []responsesOutputContent `json:"summary"`
}

type responsesOutputContent struct {
	Type        string           `json:"type"`
	Text        string           `json:"text"`
	Refusal     string           `json:"refusal"`
	Annotations []map[string]any `json:"annotations,omitempty"`
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

type openAIProviderState struct {
	ResponseID string                `json:"response_id,omitempty"`
	Output     []responsesOutputItem `json:"output,omitempty"`
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
		client = &http.Client{Timeout: 5 * time.Minute}
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

// DescribeImage implements [agent.ImageDescriptionBackend].
func (d *Driver) DescribeImage(ctx context.Context, req agent.ImageDescriptionRequest) (agent.ImageDescriptionResponse, error) {
	payload, err := d.buildDescribeImageRequest(req)
	if err != nil {
		return agent.ImageDescriptionResponse{}, err
	}
	response, err := d.doResponsesRequest(ctx, payload)
	if err != nil {
		return agent.ImageDescriptionResponse{}, err
	}
	if refusal := extractResponsesRefusal(response.Output); refusal != "" {
		return agent.ImageDescriptionResponse{}, fmt.Errorf("openai image description refused request: %s", refusal)
	}
	description := strings.TrimSpace(response.OutputText)
	if description == "" {
		description = strings.TrimSpace(extractResponsesOutputText(response.Output))
	}
	if description == "" {
		return agent.ImageDescriptionResponse{}, fmt.Errorf("openai image description response was empty")
	}
	return agent.ImageDescriptionResponse{
		Provider:    "openai",
		Model:       d.model,
		Description: description,
	}, nil
}

func (d *Driver) nextResponses(ctx context.Context, req agent.Request, caps openAIAdapterCapabilities) (agent.Decision, error) {
	requestBody := d.buildResponsesRequest(req, caps)
	return d.completeResponses(ctx, requestBody, req.Tools, caps)
}

func (d *Driver) buildDescribeImageRequest(req agent.ImageDescriptionRequest) (responsesImageRequest, error) {
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		prompt = "Describe this image."
	}
	imageURL := strings.TrimSpace(req.ImageURL)
	switch {
	case imageURL != "":
		if len(req.Data) != 0 {
			return responsesImageRequest{}, fmt.Errorf("image request must not include both image_url and data")
		}
		if strings.TrimSpace(req.MediaType) != "" {
			return responsesImageRequest{}, fmt.Errorf("image media type is not used for URL-backed image requests")
		}
	case len(req.Data) == 0:
		return responsesImageRequest{}, fmt.Errorf("image request must include either image_url or data")
	default:
		mediaType := strings.TrimSpace(req.MediaType)
		if mediaType == "" {
			return responsesImageRequest{}, fmt.Errorf("image media type must not be empty")
		}
		imageURL = "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(req.Data)
	}
	return responsesImageRequest{
		Model: d.model,
		Input: []responsesImageInputItem{{
			Role: agent.MessageRoleUser,
			Content: []responsesImageInputPart{
				{
					Type: "input_text",
					Text: prompt,
				},
				{
					Type:     "input_image",
					ImageURL: imageURL,
					Detail:   "auto",
				},
			},
		}},
	}, nil
}

func (d *Driver) buildResponsesRequest(req agent.Request, caps openAIAdapterCapabilities) responsesRequest {
	state, _ := decodeOpenAIProviderState(req.ProviderState)
	usePreviousResponseID := shouldUsePreviousResponseID(req, caps, state)
	requestBody := responsesRequest{
		Model:                d.model,
		Instructions:         buildResponsesInstructions(req.Messages, req.Tools, caps),
		Input:                buildResponsesInput(req, caps, state, usePreviousResponseID),
		Text:                 d.responsesTextConfig(req),
		PromptCacheKey:       d.promptCacheKey,
		PromptCacheRetention: responsesPromptCacheRetention(d.promptCacheRetention),
	}
	if usePreviousResponseID {
		requestBody.PreviousResponseID = state.ResponseID
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
	response, err := d.doResponsesRequest(ctx, payload)
	if err != nil {
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

func (d *Driver) doResponsesRequest(ctx context.Context, payload any) (responsesResponse, error) {
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return responsesResponse{}, fmt.Errorf("encode openai request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+"/responses", &body)
	if err != nil {
		return responsesResponse{}, fmt.Errorf("build openai request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+d.apiKey)
	req.Header.Set("Content-Type", "application/json")
	agent.MaybeWriteDebugPrompt(body.Bytes())

	resp, err := d.client.Do(req)
	if err != nil {
		return responsesResponse{}, fmt.Errorf("send openai request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return responsesResponse{}, fmt.Errorf("read openai response: %w", err)
	}
	agent.MaybeWriteDebugResponse(respBody)
	if resp.StatusCode/100 != 2 {
		var apiErr apiErrorResponse
		if err := json.Unmarshal(respBody, &apiErr); err == nil && apiErr.Error.Message != "" {
			return responsesResponse{}, fmt.Errorf("openai api error (%s): %s", resp.Status, apiErr.Error.Message)
		}
		return responsesResponse{}, fmt.Errorf("openai api error (%s): %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	var response responsesResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return responsesResponse{}, fmt.Errorf("decode openai response: %w", err)
	}
	return response, nil
}

func parseResponsesDecision(response responsesResponse, allowedTools []agent.ToolDefinition, caps openAIAdapterCapabilities) (agent.Decision, error) {
	if refusal := extractResponsesRefusal(response.Output); refusal != "" {
		return agent.Decision{}, fmt.Errorf("openai response refused request: %s", refusal)
	}
	finalText := strings.TrimSpace(response.OutputText)
	if finalText == "" {
		finalText = strings.TrimSpace(extractResponsesOutputText(response.Output))
	}
	finalReasoning := extractResponsesReasoningThought(response.Output)
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
		decision.ReplayData = encodeOpenAIReplayOutputItems(response.Output)
		decision.ProviderState = encodeOpenAIProviderState(response)
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
	decision, err := parseStrictFinalDecision(text)
	if err != nil {
		return agent.Decision{}, err
	}
	decision.ProviderState = encodeOpenAIProviderState(response)
	decision.Thought = mergeResponsesThoughts(finalReasoning, decision.Thought)
	return decision, nil
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
		case agent.ToolBoundaryKindWebSearch:
			responseTool.Name = ""
			responseTool.Description = ""
		default:
			responseTool.Type = string(agent.ToolBoundaryKindFunction)
			responseTool.Strict = tool.Strict
			responseTool.Parameters = openAICompatibleInputSchema(tool.Parameters)
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

func openAICompatibleInputSchema(schema map[string]any) map[string]any {
	if len(schema) == 0 {
		return schema
	}
	var notes []string
	for _, keyword := range []string{"oneOf", "anyOf", "allOf", "enum", "not"} {
		value, ok := schema[keyword]
		if !ok {
			continue
		}
		notes = append(notes, openAITopLevelSchemaConstraintNote(keyword, value))
	}
	if len(notes) == 0 {
		return schema
	}
	clone := make(map[string]any, len(schema))
	for key, value := range schema {
		clone[key] = value
	}
	delete(clone, "oneOf")
	delete(clone, "anyOf")
	delete(clone, "allOf")
	delete(clone, "enum")
	delete(clone, "not")
	description, _ := clone["description"].(string)
	description = strings.TrimSpace(description)
	note := strings.Join(notes, " ")
	switch {
	case description == "":
		clone["description"] = note
	default:
		clone["description"] = description + "\n\n" + note
	}
	return clone
}

func openAITopLevelSchemaConstraintNote(keyword string, value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("OpenAI compatibility note: obey the original top-level %s constraint when constructing tool input.", keyword)
	}
	return fmt.Sprintf("OpenAI compatibility note: obey the original top-level %s constraint when constructing tool input: %s", keyword, string(encoded))
}

func buildResponsesInstructions(messages []agent.Message, tools []agent.ToolDefinition, caps openAIAdapterCapabilities) string {
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
	if hasOpenAIBuiltInTools(tools, caps) {
		parts = append(parts, "Provider-native built-in tools may run internally before the final response. These do not count as runtime tool calls; only emit a runtime tool call when the runtime must act.")
	}
	return strings.Join(parts, "\n\n")
}

func hasOpenAIBuiltInTools(tools []agent.ToolDefinition, caps openAIAdapterCapabilities) bool {
	for _, adapter := range buildOpenAIToolAdapters(tools, caps) {
		switch adapter.BoundaryKind {
		case agent.ToolBoundaryKindWebSearch, agent.ToolBoundaryKindLocalShell:
			return true
		}
	}
	return false
}

func shouldUsePreviousResponseID(req agent.Request, caps openAIAdapterCapabilities, state openAIProviderState) bool {
	if strings.TrimSpace(state.ResponseID) == "" {
		return false
	}
	if openAIProviderStateAwaitingToolOutput(state) {
		_, ok := buildResponsesContinuationOutputItem(req, caps)
		return ok
	}
	if req.Step <= 1 {
		return true
	}
	_, ok := buildResponsesContinuationOutputItem(req, caps)
	return ok
}

func buildResponsesInput(req agent.Request, caps openAIAdapterCapabilities, state openAIProviderState, usePreviousResponseID bool) []responsesInputItem {
	if len(req.ConversationTurns) == 0 && len(req.CurrentTurnMessages) == 0 {
		if usePreviousResponseID {
			return buildResponsesLegacyContinuationInput(req, caps)
		}
		return buildResponsesLegacyReplayInput(req, caps, state)
	}
	if usePreviousResponseID {
		return buildResponsesContinuationInput(req, caps)
	}
	return buildResponsesReplayInput(req, caps, state)
}

func buildResponsesLegacyReplayInput(req agent.Request, caps openAIAdapterCapabilities, state openAIProviderState) []responsesInputItem {
	if len(req.Messages) == 0 {
		return nil
	}
	prefix, footer := splitRequestMessages(req.Messages)
	replays := agent.BuildStepReplays(req.Steps)
	input := make([]responsesInputItem, 0, len(req.Messages)+len(replays)*3)
	assistantReplacementIdx := latestAssistantReplacementIndex(prefix, req.Step, state)
	for idx, message := range prefix {
		if message.Role == agent.MessageRoleSystem {
			continue
		}
		if idx == assistantReplacementIdx {
			input = append(input, responsesOutputItemsAsInput(state.Output)...)
			continue
		}
		input = append(input, responsesAgentMessageInput(message))
	}
	adapters := openAIToolAdaptersByInternalName(req.Tools, caps)
	for _, replay := range replays {
		adapter, ok := adapters[replay.ToolName]
		input = append(input, buildResponsesReplayItems(replay, adapter, ok, req.StepReplayData[replay.CallID])...)
	}
	if footer.Role != agent.MessageRoleSystem {
		input = append(input, responsesAgentMessageInput(footer))
	}
	return input
}

func buildResponsesReplayInput(req agent.Request, caps openAIAdapterCapabilities, state openAIProviderState) []responsesInputItem {
	currentTurn := openAICurrentTurnMessages(req)
	if len(currentTurn) == 0 && len(req.ConversationTurns) == 0 {
		return nil
	}
	currentUser, protocol, currentState := openAICurrentTurnMessageParts(req)
	currentTurnReplays := agent.BuildStepReplays(openAIRequestCurrentTurnSteps(req))
	input := make([]responsesInputItem, 0, len(req.Messages)+len(req.Steps)*3)
	replaceLatestAssistant := req.Step <= 1 && len(state.Output) > 0
	for idx, turn := range req.ConversationTurns {
		input = appendResponsesTurnInput(input, turn, caps, req.Tools, req.StepReplayData)
		if replaceLatestAssistant && idx == len(req.ConversationTurns)-1 && turn.Assistant != nil && strings.TrimSpace(turn.Assistant.Content) != "" {
			input = input[:len(input)-1]
			input = append(input, responsesOutputItemsAsInput(state.Output)...)
			replaceLatestAssistant = false
		}
	}
	if strings.TrimSpace(currentUser.Content) != "" {
		input = append(input, responsesAgentMessageInput(currentUser))
	}
	if strings.TrimSpace(protocol.Content) != "" {
		input = append(input, responsesAgentMessageInput(protocol))
	}
	adapters := openAIToolAdaptersByInternalName(req.Tools, caps)
	for _, replay := range currentTurnReplays {
		adapter, ok := adapters[replay.ToolName]
		input = append(input, buildResponsesReplayItems(replay, adapter, ok, req.StepReplayData[replay.CallID])...)
	}
	if strings.TrimSpace(currentState.Content) != "" {
		input = append(input, responsesAgentMessageInput(currentState))
	}
	return input
}

func buildResponsesContinuationInput(req agent.Request, caps openAIAdapterCapabilities) []responsesInputItem {
	input := make([]responsesInputItem, 0, 3)
	currentUser, _, currentState := openAICurrentTurnMessageParts(req)
	if req.Step <= 1 {
		if strings.TrimSpace(currentUser.Content) != "" {
			input = append(input, responsesAgentMessageInput(currentUser))
		}
		if strings.TrimSpace(currentState.Content) != "" {
			input = append(input, responsesAgentMessageInput(currentState))
		}
		return input
	}
	if outputItem, ok := buildResponsesContinuationOutputItem(req, caps); ok {
		input = append(input, outputItem)
	}
	if strings.TrimSpace(currentState.Content) != "" {
		input = append(input, responsesAgentMessageInput(currentState))
	}
	return input
}

func buildResponsesLegacyContinuationInput(req agent.Request, caps openAIAdapterCapabilities) []responsesInputItem {
	input := make([]responsesInputItem, 0, 3)
	if req.Step <= 1 {
		currentTurn, currentState := currentTurnContinuationMessages(req.Messages)
		if currentTurn.Content != "" {
			input = append(input, responsesAgentMessageInput(currentTurn))
		}
		if currentState.Content != "" {
			input = append(input, responsesAgentMessageInput(currentState))
		}
		return input
	}
	if outputItem, ok := buildResponsesLegacyContinuationOutputItem(req, caps); ok {
		input = append(input, outputItem)
	}
	if currentState, ok := lastNonSystemMessage(req.Messages); ok {
		input = append(input, responsesAgentMessageInput(currentState))
	}
	return input
}

func splitRequestMessages(messages []agent.Message) ([]agent.Message, agent.Message) {
	if len(messages) == 0 {
		return nil, agent.Message{}
	}
	return messages[:len(messages)-1], messages[len(messages)-1]
}

func buildResponsesReplayItems(replay agent.StepReplay, adapter openAIToolAdapter, ok bool, rawReplay string) []responsesInputItem {
	if items, replayOK := buildResponsesNativeReplayItems(replay, rawReplay); replayOK {
		return items
	}
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

func buildResponsesNativeReplayItems(replay agent.StepReplay, rawReplay string) ([]responsesInputItem, bool) {
	outputItems, ok := decodeOpenAIReplayOutputItems(rawReplay)
	if !ok {
		return nil, false
	}
	input := responsesOutputItemsAsInput(outputItems)
	toolCall, ok := latestResponsesToolCall(outputItems)
	if !ok || strings.TrimSpace(toolCall.CallID) == "" {
		return nil, false
	}
	outputType := "function_call_output"
	if toolCall.Type == "custom_tool_call" {
		outputType = "custom_tool_call_output"
	}
	input = append(input, responsesInputItem{
		Type:   outputType,
		CallID: toolCall.CallID,
		Output: replay.Output,
	})
	return input, true
}

func buildResponsesContinuationOutputItem(req agent.Request, caps openAIAdapterCapabilities) (responsesInputItem, bool) {
	replay, ok := latestStepReplay(openAIRequestCurrentTurnSteps(req))
	if !ok {
		return responsesInputItem{}, false
	}
	adapter, adapterOK := openAIToolAdaptersByInternalName(req.Tools, caps)[replay.ToolName]
	outputItems, replayOK := decodeOpenAIReplayOutputItems(req.StepReplayData[replay.CallID])
	if !replayOK {
		return responsesInputItem{}, false
	}
	toolCall, ok := latestResponsesToolCall(outputItems)
	if !ok || strings.TrimSpace(toolCall.CallID) == "" {
		return responsesInputItem{}, false
	}
	switch {
	case toolCall.Type == "custom_tool_call":
		return responsesInputItem{
			Type:   "custom_tool_call_output",
			CallID: toolCall.CallID,
			Output: replay.Output,
		}, true
	case adapterOK && openAIReplayBoundaryKind(replay, adapter, true) == agent.ToolBoundaryKindCustom:
		return responsesInputItem{
			Type:   "custom_tool_call_output",
			CallID: toolCall.CallID,
			Output: replay.Output,
		}, true
	default:
		return responsesInputItem{
			Type:   "function_call_output",
			CallID: toolCall.CallID,
			Output: replay.Output,
		}, true
	}
}

func buildResponsesLegacyContinuationOutputItem(req agent.Request, caps openAIAdapterCapabilities) (responsesInputItem, bool) {
	replay, ok := latestStepReplay(req.Steps)
	if !ok {
		return responsesInputItem{}, false
	}
	adapter, adapterOK := openAIToolAdaptersByInternalName(req.Tools, caps)[replay.ToolName]
	outputItems, replayOK := decodeOpenAIReplayOutputItems(req.StepReplayData[replay.CallID])
	if !replayOK {
		return responsesInputItem{}, false
	}
	toolCall, ok := latestResponsesToolCall(outputItems)
	if !ok || strings.TrimSpace(toolCall.CallID) == "" {
		return responsesInputItem{}, false
	}
	switch {
	case toolCall.Type == "custom_tool_call":
		return responsesInputItem{
			Type:   "custom_tool_call_output",
			CallID: toolCall.CallID,
			Output: replay.Output,
		}, true
	case adapterOK && openAIReplayBoundaryKind(replay, adapter, true) == agent.ToolBoundaryKindCustom:
		return responsesInputItem{
			Type:   "custom_tool_call_output",
			CallID: toolCall.CallID,
			Output: replay.Output,
		}, true
	default:
		return responsesInputItem{
			Type:   "function_call_output",
			CallID: toolCall.CallID,
			Output: replay.Output,
		}, true
	}
}

func latestStepReplay(steps []agent.Step) (agent.StepReplay, bool) {
	replays := agent.BuildStepReplays(steps)
	if len(replays) == 0 {
		return agent.StepReplay{}, false
	}
	return replays[len(replays)-1], true
}

func currentTurnContinuationMessages(messages []agent.Message) (agent.Message, agent.Message) {
	currentState, ok := lastNonSystemMessage(messages)
	if !ok {
		return agent.Message{}, agent.Message{}
	}
	for idx := len(messages) - 1; idx >= 0; idx-- {
		message := messages[idx]
		if message.Role == agent.MessageRoleSystem || strings.TrimSpace(message.Content) == "" {
			continue
		}
		if message.Content == currentState.Content {
			continue
		}
		if isTurnProtocolMessage(message) {
			continue
		}
		return message, currentState
	}
	return agent.Message{}, currentState
}

func latestAssistantReplacementIndex(messages []agent.Message, step int, state openAIProviderState) int {
	if step > 1 || len(state.Output) == 0 {
		return -1
	}
	currentTurn, _ := currentTurnContinuationMessages(messages)
	if strings.TrimSpace(currentTurn.Content) == "" {
		return -1
	}
	for idx := len(messages) - 1; idx >= 0; idx-- {
		message := messages[idx]
		if message.Role != agent.MessageRoleAssistant {
			continue
		}
		return idx
	}
	return -1
}

func lastNonSystemMessage(messages []agent.Message) (agent.Message, bool) {
	for idx := len(messages) - 1; idx >= 0; idx-- {
		message := messages[idx]
		if message.Role == agent.MessageRoleSystem || strings.TrimSpace(message.Content) == "" {
			continue
		}
		return message, true
	}
	return agent.Message{}, false
}

func isTurnProtocolMessage(message agent.Message) bool {
	content := strings.TrimSpace(message.Content)
	if message.Role != agent.MessageRoleUser || content == "" {
		return false
	}
	return strings.Contains(content, "Use exactly one tool call when more work is needed") &&
		strings.Contains(content, "Never emit multiple tool calls in a single response.")
}

func openAICurrentTurnMessages(req agent.Request) []agent.Message {
	if len(req.CurrentTurnMessages) > 0 {
		return append([]agent.Message(nil), req.CurrentTurnMessages...)
	}
	nonSystem := make([]agent.Message, 0, len(req.Messages))
	for _, message := range req.Messages {
		if message.Role == agent.MessageRoleSystem {
			continue
		}
		nonSystem = append(nonSystem, message)
	}
	if len(nonSystem) <= 3 {
		return nonSystem
	}
	return append([]agent.Message(nil), nonSystem[len(nonSystem)-3:]...)
}

func openAICurrentTurnMessageParts(req agent.Request) (agent.Message, agent.Message, agent.Message) {
	messages := openAICurrentTurnMessages(req)
	switch len(messages) {
	case 0:
		return agent.Message{}, agent.Message{}, agent.Message{}
	case 1:
		return messages[0], agent.Message{}, agent.Message{}
	case 2:
		return messages[0], agent.Message{}, messages[1]
	default:
		return messages[0], messages[1], messages[2]
	}
}

func openAIRequestCurrentTurnSteps(req agent.Request) []agent.Step {
	if len(req.CurrentTurnSteps) > 0 || len(req.ConversationTurns) > 0 {
		return req.CurrentTurnSteps
	}
	return req.Steps
}

func appendResponsesTurnInput(
	input []responsesInputItem,
	turn agent.ConversationTurn,
	caps openAIAdapterCapabilities,
	tools []agent.ToolDefinition,
	stepReplayData map[string]string,
) []responsesInputItem {
	if strings.TrimSpace(turn.User.Content) != "" {
		input = append(input, responsesAgentMessageInput(turn.User))
	}
	adapters := openAIToolAdaptersByInternalName(tools, caps)
	for _, replay := range agent.BuildStepReplays(turn.Steps) {
		adapter, ok := adapters[replay.ToolName]
		input = append(input, buildResponsesReplayItems(replay, adapter, ok, stepReplayData[replay.CallID])...)
	}
	if turn.Assistant != nil && strings.TrimSpace(turn.Assistant.Content) != "" {
		input = append(input, responsesAgentMessageInput(*turn.Assistant))
	}
	return input
}

func responsesAgentMessageInput(message agent.Message) responsesInputItem {
	return responsesInputItem{
		Role:    message.Role,
		Content: message.Content,
	}
}

func responsesOutputItemsAsInput(items []responsesOutputItem) []responsesInputItem {
	if len(items) == 0 {
		return nil
	}
	input := make([]responsesInputItem, 0, len(items))
	for _, item := range items {
		switch item.Type {
		case "message":
			input = append(input, responsesInputItem{
				Type:          item.Type,
				Role:          item.Role,
				Phase:         item.Phase,
				ContentBlocks: cloneResponsesOutputContent(item.Content),
			})
		case "reasoning":
			input = append(input, responsesInputItem{
				Type:          item.Type,
				ContentBlocks: cloneResponsesOutputContent(item.Content),
				Summary:       cloneResponsesOutputContent(item.Summary),
			})
		case "function_call":
			input = append(input, responsesInputItem{
				Type:      item.Type,
				CallID:    item.CallID,
				Name:      item.Name,
				Arguments: item.Arguments,
			})
		case "custom_tool_call":
			input = append(input, responsesInputItem{
				Type:   item.Type,
				CallID: item.CallID,
				Name:   item.Name,
				Input:  item.Input,
			})
		}
	}
	return input
}

func latestResponsesToolCall(items []responsesOutputItem) (responsesOutputItem, bool) {
	for idx := len(items) - 1; idx >= 0; idx-- {
		switch items[idx].Type {
		case "function_call", "custom_tool_call":
			return items[idx], true
		}
	}
	return responsesOutputItem{}, false
}

func decodeOpenAIReplayOutputItems(raw string) ([]responsesOutputItem, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}
	var items []responsesOutputItem
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, false
	}
	if len(items) == 0 {
		return nil, false
	}
	return items, true
}

func cloneResponsesOutputContent(content []responsesOutputContent) []responsesOutputContent {
	if len(content) == 0 {
		return nil
	}
	cloned := make([]responsesOutputContent, len(content))
	copy(cloned, content)
	return cloned
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
		SupportsCustomTools:     true,
		SupportsHostedWebSearch: supportsResponsesHostedWebSearch(d.model, d.reasoningEffort),
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

func supportsResponsesHostedWebSearch(model, effort string) bool {
	model = strings.TrimSpace(model)
	effort = strings.TrimSpace(effort)
	switch {
	case strings.HasPrefix(model, "gpt-5") && effort == "minimal":
		return false
	case strings.HasPrefix(model, "gpt-4.1-nano"):
		return false
	case strings.HasPrefix(model, "gpt-5"),
		strings.HasPrefix(model, "gpt-4.1"),
		strings.HasPrefix(model, "gpt-4o"),
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

func decodeOpenAIProviderState(raw string) (openAIProviderState, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return openAIProviderState{}, false
	}
	var state openAIProviderState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return openAIProviderState{}, false
	}
	if strings.TrimSpace(state.ResponseID) == "" && len(state.Output) == 0 {
		return openAIProviderState{}, false
	}
	return state, true
}

func openAIProviderStateAwaitingToolOutput(state openAIProviderState) bool {
	toolCall, ok := latestResponsesToolCall(state.Output)
	if !ok {
		return false
	}
	return strings.TrimSpace(toolCall.CallID) != ""
}

func encodeOpenAIProviderState(response responsesResponse) string {
	state := openAIProviderState{
		ResponseID: strings.TrimSpace(response.ID),
		Output:     cloneResponsesOutputItems(response.Output),
	}
	if state.ResponseID == "" && len(state.Output) == 0 {
		return ""
	}
	return compactJSON(state)
}

func encodeOpenAIReplayOutputItems(items []responsesOutputItem) string {
	if len(items) == 0 {
		return ""
	}
	return compactJSON(cloneResponsesOutputItems(items))
}

func cloneResponsesOutputItems(items []responsesOutputItem) []responsesOutputItem {
	if len(items) == 0 {
		return nil
	}
	cloned := make([]responsesOutputItem, len(items))
	for idx, item := range items {
		cloned[idx] = item
		cloned[idx].Content = cloneResponsesOutputContent(item.Content)
		cloned[idx].Summary = cloneResponsesOutputContent(item.Summary)
	}
	return cloned
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

func extractResponsesReasoningThought(items []responsesOutputItem) string {
	var b strings.Builder
	for _, item := range items {
		if item.Type != "reasoning" {
			continue
		}
		text := strings.TrimSpace(extractResponsesReasoningSummaryText(item))
		if text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(text)
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

func mergeResponsesThoughts(parts ...string) string {
	var merged []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if len(merged) > 0 && merged[len(merged)-1] == part {
			continue
		}
		merged = append(merged, part)
	}
	return strings.Join(merged, "\n")
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
