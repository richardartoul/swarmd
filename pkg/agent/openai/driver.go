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

type apiErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
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
	model, reasoningEffort := SplitModelReasoningEffort(cfg.Model)
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

func (d *Driver) adapterCapabilities() openAIAdapterCapabilities {
	return openAIAdapterCapabilities{
		SupportsCustomTools:     true,
		SupportsHostedWebSearch: supportsResponsesHostedWebSearch(d.model, d.reasoningEffort),
	}
}
