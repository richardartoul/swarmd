// See LICENSE for licensing information

// Package anthropic provides an Anthropic-backed implementation of [agent.Driver].
package anthropic

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
	// DefaultBaseURL is the default Anthropic API base URL.
	DefaultBaseURL = "https://api.anthropic.com/v1"

	// DefaultMaxTokens is the built-in Anthropic output budget used by this driver.
	DefaultMaxTokens = 64000

	anthropicVersion                    = "2023-06-01"
	anthropicReplayCacheMinApproxTokens = 1024
	anthropicMaxContinuations           = 3
	anthropicDiagnosticContentLimit     = 512

	anthropicEmptyEndTurnContinuationPrompt       = "Please continue and respond with either exactly one tool call or the strict final JSON response."
	anthropicTruncationContinuationPrompt         = "Please continue from where you left off. Do not repeat prior content. Respond with either exactly one tool call or the strict final JSON response."
	anthropicDescribeImageEmptyContinuationPrompt = "Please continue describing the image in plain text."
	anthropicDescribeImageTruncationPrompt        = "Please continue describing the image in plain text from where you left off. Do not repeat prior content."
)

// DefaultSystemPrompt is kept for backward compatibility.
var DefaultSystemPrompt = agent.DefaultSystemPrompt

// Config configures a new Anthropic-backed driver.
type Config struct {
	APIKey         string
	BaseURL        string
	Model          string
	HTTPClient     *http.Client
	PromptCacheTTL string

	// Deprecated: configure the prompt on [agent.Config].
	SystemPrompt string
}

// Driver implements [agent.Driver] using the Anthropic Messages API.
type Driver struct {
	apiKey         string
	baseURL        string
	model          string
	reasoning      string
	client         *http.Client
	maxTokens      int
	promptCacheTTL string
}

type apiErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
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
	promptCacheTTL, err := normalizePromptCacheTTL(cfg.PromptCacheTTL)
	if err != nil {
		return nil, err
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	model, reasoning := parseModelAndReasoningLevel(cfg.Model)
	if !supportsAnthropicStructuredOutputs(model) {
		return nil, fmt.Errorf("anthropic model %q must support structured outputs", model)
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	return &Driver{
		apiKey:         cfg.APIKey,
		baseURL:        baseURL,
		model:          model,
		reasoning:      reasoning,
		client:         client,
		maxTokens:      DefaultMaxTokens,
		promptCacheTTL: promptCacheTTL,
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

// DescribeImage implements [agent.ImageDescriptionBackend].
func (d *Driver) DescribeImage(ctx context.Context, req agent.ImageDescriptionRequest) (agent.ImageDescriptionResponse, error) {
	payload, err := d.buildDescribeImageRequest(req)
	if err != nil {
		return agent.ImageDescriptionResponse{}, err
	}
	response, err := d.completeDescribeImage(ctx, payload)
	if err != nil {
		return agent.ImageDescriptionResponse{}, err
	}
	description := strings.TrimSpace(extractAnthropicText(response.Content))
	if description == "" {
		return agent.ImageDescriptionResponse{}, fmt.Errorf("anthropic image description response was empty")
	}
	return agent.ImageDescriptionResponse{
		Provider:    "anthropic",
		Model:       d.model,
		Description: description,
	}, nil
}

func (d *Driver) buildMessagesRequest(req agent.Request) (messagesRequest, error) {
	payload := messagesRequest{
		Model:     d.model,
		MaxTokens: d.maxTokens,
		OutputConfig: &anthropicOutputConfig{
			Format: &anthropicOutputFormat{
				Type:   "json_schema",
				Schema: agent.StrictFinalResponseSchema(),
			},
		},
	}
	if d.promptCacheTTL != "" {
		payload.CacheControl = anthropicPromptCacheControl(d.promptCacheTTL)
	}
	if d.reasoning != "" {
		payload.Thinking = &anthropicThinking{Type: "adaptive"}
		payload.OutputConfig.Effort = d.reasoning
	}
	system, messages, err := buildAnthropicMessages(req, d.promptCacheTTL, d.model)
	if err != nil {
		return messagesRequest{}, err
	}
	payload.System = system
	payload.Messages = messages
	if len(payload.Messages) == 0 {
		return messagesRequest{}, fmt.Errorf("anthropic request must include at least one non-system message")
	}
	if len(req.Tools) > 0 {
		payload.Tools = buildAnthropicTools(req.Tools, d.promptCacheTTL, len(payload.System) == 0)
		payload.ToolChoice = &anthropicToolChoice{
			Type:                   "auto",
			DisableParallelToolUse: true,
		}
	}
	return payload, nil
}

func (d *Driver) buildDescribeImageRequest(req agent.ImageDescriptionRequest) (messagesRequest, error) {
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		prompt = "Describe this image."
	}
	var source anthropicRequestImageSource
	imageURL := strings.TrimSpace(req.ImageURL)
	switch {
	case imageURL != "":
		if len(req.Data) != 0 {
			return messagesRequest{}, fmt.Errorf("image request must not include both image_url and data")
		}
		if strings.TrimSpace(req.MediaType) != "" {
			return messagesRequest{}, fmt.Errorf("image media type is not used for URL-backed image requests")
		}
		source = anthropicRequestImageSource{
			Type: "url",
			URL:  imageURL,
		}
	case len(req.Data) == 0:
		return messagesRequest{}, fmt.Errorf("image request must include either image_url or data")
	default:
		mediaType := strings.TrimSpace(req.MediaType)
		if mediaType == "" {
			return messagesRequest{}, fmt.Errorf("image media type must not be empty")
		}
		source = anthropicRequestImageSource{
			Type:      "base64",
			MediaType: mediaType,
			Data:      base64.StdEncoding.EncodeToString(req.Data),
		}
	}
	return messagesRequest{
		Model:     d.model,
		MaxTokens: d.maxTokens,
		Messages: []anthropicMessage{{
			Role: agent.MessageRoleUser,
			Content: []any{
				anthropicRequestTextBlock{
					Type: "text",
					Text: prompt,
				},
				anthropicRequestImageBlock{
					Type:   "image",
					Source: source,
				},
			},
		}},
	}, nil
}

func (d *Driver) completeDescribeImage(ctx context.Context, payload messagesRequest) (messagesResponse, error) {
	var combined messagesResponse
	for attempt := 0; ; attempt++ {
		response, err := d.completeOnce(ctx, payload)
		if err != nil {
			return messagesResponse{}, err
		}
		stopReason := strings.TrimSpace(response.StopReason)
		if stopReason == "refusal" {
			return messagesResponse{}, fmt.Errorf("anthropic image description refused request (%s)", anthropicResponseSummary(stopReason, response.Content))
		}
		combined.Content = append(combined.Content, response.Content...)
		combined.StopReason = response.StopReason
		combined.Usage.InputTokens += response.Usage.InputTokens
		combined.Usage.OutputTokens += response.Usage.OutputTokens
		combined.Usage.CacheReadInputTokens += response.Usage.CacheReadInputTokens
		switch {
		case stopReason == "pause_turn":
			if len(response.Content) == 0 {
				return messagesResponse{}, fmt.Errorf("anthropic image description response with stop_reason %q was empty (%s)", stopReason, anthropicResponseSummary(stopReason, response.Content))
			}
			if attempt >= anthropicMaxContinuations {
				return messagesResponse{}, fmt.Errorf("anthropic image description exceeded pause_turn continuation limit (%s)", anthropicResponseSummary(stopReason, response.Content))
			}
			payload = anthropicContinuationPayload(payload, response, anthropicDescribeImageTruncationPrompt)
			continue
		case stopReason == "end_turn" && strings.TrimSpace(extractAnthropicText(response.Content)) == "":
			if attempt >= anthropicMaxContinuations {
				return messagesResponse{}, fmt.Errorf("anthropic image description exceeded empty end_turn continuation limit (%s)", anthropicResponseSummary(stopReason, response.Content))
			}
			payload = anthropicContinuationPayload(payload, response, anthropicDescribeImageEmptyContinuationPrompt)
			continue
		case anthropicStopReasonNeedsContinuation(stopReason):
			if attempt >= anthropicMaxContinuations {
				return messagesResponse{}, fmt.Errorf("anthropic image description exceeded %q continuation limit (%s)", stopReason, anthropicResponseSummary(stopReason, response.Content))
			}
			payload = anthropicContinuationPayload(payload, response, anthropicDescribeImageTruncationPrompt)
			continue
		default:
			return combined, nil
		}
	}
}

func (d *Driver) complete(ctx context.Context, payload messagesRequest, allowedTools []agent.ToolDefinition) (agent.Decision, error) {
	var totalUsage agent.Usage
	for attempt := 0; ; attempt++ {
		response, err := d.completeOnce(ctx, payload)
		if err != nil {
			return agent.Decision{}, err
		}
		// Continuation round-trips are part of the same decision; report
		// their combined usage.
		totalUsage = totalUsage.Add(response.Usage.toAgentUsage())

		stopReason := strings.TrimSpace(response.StopReason)
		switch stopReason {
		case "refusal":
			return agent.Decision{}, fmt.Errorf("anthropic response refused request (%s)", anthropicResponseSummary(stopReason, response.Content))
		case "pause_turn":
			if len(response.Content) == 0 {
				return agent.Decision{}, fmt.Errorf("anthropic response with stop_reason %q was empty (%s)", stopReason, anthropicResponseSummary(stopReason, response.Content))
			}
			if attempt >= anthropicMaxContinuations {
				return agent.Decision{}, fmt.Errorf("anthropic response exceeded pause_turn continuation limit (%s)", anthropicResponseSummary(stopReason, response.Content))
			}
			payload = anthropicContinuationPayload(payload, response, "")
			continue
		}

		if stopReason == "end_turn" && !anthropicResponseHasDecisionSignal(response) {
			if attempt >= anthropicMaxContinuations {
				return agent.Decision{}, fmt.Errorf("anthropic response exceeded empty end_turn continuation limit (%s)", anthropicResponseSummary(stopReason, response.Content))
			}
			payload = anthropicContinuationPayload(payload, response, anthropicEmptyEndTurnContinuationPrompt)
			continue
		}
		if anthropicStopReasonNeedsContinuation(stopReason) && !anthropicResponseHasToolUse(response) {
			if attempt >= anthropicMaxContinuations {
				return agent.Decision{}, fmt.Errorf("anthropic response exceeded %q continuation limit (%s)", stopReason, anthropicResponseSummary(stopReason, response.Content))
			}
			payload = anthropicContinuationPayload(payload, response, anthropicTruncationContinuationPrompt)
			continue
		}

		decision, err := parseMessageDecision(response, allowedTools)
		if err != nil {
			return agent.Decision{}, anthropicResponseParseError{
				StopReason: stopReason,
				Content:    response.Content,
				Err:        err,
			}
		}
		decision.Usage = totalUsage
		return decision, nil
	}
}

func (d *Driver) completeOnce(ctx context.Context, payload messagesRequest) (messagesResponse, error) {
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return messagesResponse{}, fmt.Errorf("encode anthropic request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+"/messages", &body)
	if err != nil {
		return messagesResponse{}, fmt.Errorf("build anthropic request: %w", err)
	}
	req.Header.Set("x-api-key", d.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("Content-Type", "application/json")
	agent.MaybeWriteDebugPrompt(body.Bytes())

	resp, err := d.client.Do(req)
	if err != nil {
		return messagesResponse{}, fmt.Errorf("send anthropic request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return messagesResponse{}, fmt.Errorf("read anthropic response: %w", err)
	}
	agent.MaybeWriteDebugResponse(respBody)
	if resp.StatusCode/100 != 2 {
		var apiErr apiErrorResponse
		if err := json.Unmarshal(respBody, &apiErr); err == nil && apiErr.Error.Message != "" {
			return messagesResponse{}, fmt.Errorf("anthropic api error (%s): %s", resp.Status, apiErr.Error.Message)
		}
		return messagesResponse{}, fmt.Errorf("anthropic api error (%s): %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	var response messagesResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return messagesResponse{}, fmt.Errorf("decode anthropic response: %w", err)
	}
	return response, nil
}

type anthropicResponseParseError struct {
	StopReason string
	Content    []anthropicContentBlock
	Err        error
}

func (e anthropicResponseParseError) Error() string {
	return fmt.Sprintf("anthropic response parse error (%s): %v", anthropicResponseSummary(e.StopReason, e.Content), e.Err)
}

func (e anthropicResponseParseError) Unwrap() error {
	return e.Err
}

func anthropicContinuationPayload(payload messagesRequest, response messagesResponse, userPrompt string) messagesRequest {
	next := payload
	next.Messages = append([]anthropicMessage(nil), payload.Messages...)
	if len(response.Content) > 0 {
		next.Messages = append(next.Messages, anthropicMessage{
			Role:    agent.MessageRoleAssistant,
			Content: response.Content,
		})
	}
	userPrompt = strings.TrimSpace(userPrompt)
	if userPrompt != "" {
		next.Messages = append(next.Messages, anthropicMessage{
			Role:    agent.MessageRoleUser,
			Content: userPrompt,
		})
	}
	return next
}

func anthropicStopReasonNeedsContinuation(stopReason string) bool {
	switch strings.TrimSpace(stopReason) {
	case "max_tokens", "model_context_window_exceeded":
		return true
	default:
		return false
	}
}

func anthropicResponseHasDecisionSignal(response messagesResponse) bool {
	return anthropicResponseHasToolUse(response) || extractAnthropicText(response.Content) != ""
}

func anthropicResponseHasToolUse(response messagesResponse) bool {
	for _, block := range response.Content {
		if block.Type == "tool_use" {
			return true
		}
	}
	return false
}

func anthropicResponseSummary(stopReason string, content []anthropicContentBlock) string {
	return fmt.Sprintf(
		`stop_reason=%q block_types=%s content=%s`,
		strings.TrimSpace(stopReason),
		compactJSON(anthropicContentBlockTypes(content)),
		anthropicDiagnosticContent(content),
	)
}

func anthropicContentBlockTypes(blocks []anthropicContentBlock) []string {
	if len(blocks) == 0 {
		return []string{}
	}
	types := make([]string, 0, len(blocks))
	for _, block := range blocks {
		typeName := strings.TrimSpace(block.Type)
		if typeName == "" {
			typeName = "<empty>"
		}
		types = append(types, typeName)
	}
	return types
}

func anthropicDiagnosticContent(blocks []anthropicContentBlock) string {
	if len(blocks) == 0 {
		return "[]"
	}
	return truncateAnthropicDiagnostic(compactJSON(blocks), anthropicDiagnosticContentLimit)
}

func truncateAnthropicDiagnostic(text string, maxRunes int) string {
	text = strings.TrimSpace(text)
	if text == "" || maxRunes <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	if maxRunes <= 3 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-3]) + "..."
}

func normalizePromptCacheTTL(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "":
		return "5m", nil
	case "5m", "1h":
		return strings.TrimSpace(value), nil
	default:
		return "", fmt.Errorf(`anthropic prompt cache ttl must be empty, "5m", or "1h"`)
	}
}
