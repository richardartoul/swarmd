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
	"unicode/utf8"

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

	anthropicEmptyEndTurnContinuationPrompt = "Please continue and respond with either exactly one tool call or the strict final JSON response."
	anthropicTruncationContinuationPrompt   = "Please continue from where you left off. Do not repeat prior content. Respond with either exactly one tool call or the strict final JSON response."
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

type messagesRequest struct {
	Model        string                      `json:"model"`
	MaxTokens    int                         `json:"max_tokens"`
	CacheControl *anthropicCacheControl      `json:"cache_control,omitempty"`
	System       []anthropicRequestTextBlock `json:"system,omitempty"`
	Messages     []anthropicMessage          `json:"messages"`
	Thinking     *anthropicThinking          `json:"thinking,omitempty"`
	OutputConfig *anthropicOutputConfig      `json:"output_config,omitempty"`
	Tools        []anthropicTool             `json:"tools,omitempty"`
	ToolChoice   *anthropicToolChoice        `json:"tool_choice,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type anthropicThinking struct {
	Type string `json:"type"`
}

type anthropicOutputConfig struct {
	Effort string                 `json:"effort,omitempty"`
	Format *anthropicOutputFormat `json:"format,omitempty"`
}

type anthropicOutputFormat struct {
	Type   string         `json:"type"`
	Schema map[string]any `json:"schema,omitempty"`
}

type anthropicCacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

type anthropicRequestTextBlock struct {
	Type         string                 `json:"type"`
	Text         string                 `json:"text"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicRequestToolUseBlock struct {
	Type  string         `json:"type"`
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

type anthropicRequestToolResultBlock struct {
	Type         string                 `json:"type"`
	ToolUseID    string                 `json:"tool_use_id"`
	Content      string                 `json:"content"`
	IsError      bool                   `json:"is_error,omitempty"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicTool struct {
	Name          string                 `json:"name"`
	Description   string                 `json:"description,omitempty"`
	Strict        bool                   `json:"strict,omitempty"`
	InputSchema   map[string]any         `json:"input_schema"`
	InputExamples []map[string]any       `json:"input_examples,omitempty"`
	CacheControl  *anthropicCacheControl `json:"cache_control,omitempty"`
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
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	Thinking  string          `json:"thinking"`
	Signature string          `json:"signature"`
	Data      string          `json:"data"`
	raw       json.RawMessage
}

func (b *anthropicContentBlock) UnmarshalJSON(data []byte) error {
	type rawBlock struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
		Thinking  string          `json:"thinking"`
		Signature string          `json:"signature"`
		Data      string          `json:"data"`
	}
	var decoded rawBlock
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	b.Type = decoded.Type
	b.Text = decoded.Text
	b.ID = decoded.ID
	b.Name = decoded.Name
	b.Input = append(json.RawMessage(nil), decoded.Input...)
	b.Thinking = decoded.Thinking
	b.Signature = decoded.Signature
	b.Data = decoded.Data
	b.raw = append(json.RawMessage(nil), data...)
	return nil
}

func (b anthropicContentBlock) MarshalJSON() ([]byte, error) {
	raw := b.rawJSON()
	if len(raw) == 0 {
		return []byte("null"), nil
	}
	return raw, nil
}

func (b anthropicContentBlock) rawJSON() json.RawMessage {
	if len(b.raw) > 0 {
		return append(json.RawMessage(nil), b.raw...)
	}
	type rawBlock struct {
		Type      string          `json:"type"`
		Text      string          `json:"text,omitempty"`
		ID        string          `json:"id,omitempty"`
		Name      string          `json:"name,omitempty"`
		Input     json.RawMessage `json:"input,omitempty"`
		Thinking  string          `json:"thinking,omitempty"`
		Signature string          `json:"signature,omitempty"`
		Data      string          `json:"data,omitempty"`
	}
	data, err := json.Marshal(rawBlock{
		Type:      b.Type,
		Text:      b.Text,
		ID:        b.ID,
		Name:      b.Name,
		Input:     b.Input,
		Thinking:  b.Thinking,
		Signature: b.Signature,
		Data:      b.Data,
	})
	if err != nil {
		return nil
	}
	return data
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
		client = &http.Client{Timeout: 2 * time.Minute}
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

func buildAnthropicMessages(req agent.Request, promptCacheTTL, model string) ([]anthropicRequestTextBlock, []anthropicMessage, error) {
	if len(req.Messages) == 0 {
		return nil, nil, nil
	}
	if len(req.ConversationTurns) == 0 && len(req.CurrentTurnMessages) == 0 {
		return buildAnthropicLegacyMessages(req, promptCacheTTL, model)
	}

	var systemBlocks []anthropicRequestTextBlock
	messages := make([]anthropicMessage, 0, len(req.Messages)+len(req.Steps)*2)
	for _, message := range req.Messages {
		if message.Role != agent.MessageRoleSystem || strings.TrimSpace(message.Content) == "" {
			continue
		}
		systemBlocks = append(systemBlocks, anthropicRequestTextBlock{
			Type: "text",
			Text: message.Content,
		})
	}
	cacheControl := anthropicPromptCacheControl(promptCacheTTL)
	if len(systemBlocks) > 0 && cacheControl != nil {
		systemBlocks[len(systemBlocks)-1].CacheControl = cacheControl
	}

	historyCacheAssigned := false
	for idx, turn := range req.ConversationTurns {
		lastTurn := idx == len(req.ConversationTurns)-1
		applyCacheToUser := (*anthropicCacheControl)(nil)
		if lastTurn && len(turn.Steps) == 0 && turn.Assistant == nil && !historyCacheAssigned {
			applyCacheToUser = cacheControl
		}
		if err := appendAnthropicMessage(&messages, turn.User, applyCacheToUser); err != nil {
			return nil, nil, err
		}
		if applyCacheToUser != nil {
			historyCacheAssigned = true
		}
		replays := agent.BuildStepReplays(turn.Steps)
		for replayIdx, replay := range replays {
			replayOptions := anthropicReplayOptions{
				AssistantPreamble: strings.TrimSpace(req.StepReplayData[replay.CallID]),
			}
			if lastTurn && turn.Assistant == nil && replayIdx == len(replays)-1 && !historyCacheAssigned {
				replayOptions.ResultCacheControl = cacheControl
				historyCacheAssigned = cacheControl != nil
			}
			replayMessages, err := buildAnthropicReplayMessages(replay, req.Tools, replayOptions)
			if err != nil {
				return nil, nil, err
			}
			messages = append(messages, replayMessages...)
		}
		if turn.Assistant != nil {
			assistantCacheControl := (*anthropicCacheControl)(nil)
			if lastTurn && !historyCacheAssigned {
				assistantCacheControl = cacheControl
				historyCacheAssigned = assistantCacheControl != nil
			}
			if err := appendAnthropicMessage(&messages, *turn.Assistant, assistantCacheControl); err != nil {
				return nil, nil, err
			}
		}
	}

	currentTurnMessages := append([]agent.Message(nil), req.CurrentTurnMessages...)
	if len(currentTurnMessages) == 0 {
		currentTurnMessages = fallbackAnthropicCurrentTurnMessages(req.Messages)
	}
	currentUser, protocol, footer := anthropicCurrentTurnMessages(currentTurnMessages)
	if err := appendAnthropicMessage(&messages, currentUser, historyCacheControlForCurrentUser(cacheControl, req.ConversationTurns, historyCacheAssigned)); err != nil {
		return nil, nil, err
	}
	if err := appendAnthropicMessage(&messages, protocol, nil); err != nil {
		return nil, nil, err
	}

	replays := agent.BuildStepReplays(anthropicRequestCurrentTurnSteps(req))
	cacheableReplayIdx := lastAnthropicCacheableReplayIndex(replays, req.Tools, anthropicMinimumCacheableTokens(model))
	for idx, replay := range replays {
		replayOptions := anthropicReplayOptions{
			AssistantPreamble: strings.TrimSpace(req.StepReplayData[replay.CallID]),
		}
		if idx == cacheableReplayIdx {
			replayOptions.ResultCacheControl = cacheControl
		}
		replayMessages, err := buildAnthropicReplayMessages(replay, req.Tools, replayOptions)
		if err != nil {
			return nil, nil, err
		}
		messages = append(messages, replayMessages...)
	}

	if err := appendAnthropicMessage(&messages, footer, nil); err != nil {
		return nil, nil, err
	}
	return systemBlocks, messages, nil
}

func buildAnthropicLegacyMessages(req agent.Request, promptCacheTTL, model string) ([]anthropicRequestTextBlock, []anthropicMessage, error) {
	var systemBlocks []anthropicRequestTextBlock
	prefix, footer := splitAnthropicRequestMessages(req.Messages)
	messages := make([]anthropicMessage, 0, len(req.Messages)+len(req.Steps)*2)
	appendPrepared := func(message agent.Message) error {
		switch message.Role {
		case agent.MessageRoleSystem:
			if strings.TrimSpace(message.Content) != "" {
				systemBlocks = append(systemBlocks, anthropicRequestTextBlock{
					Type: "text",
					Text: message.Content,
				})
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
			return nil, nil, err
		}
	}

	replays := agent.BuildStepReplays(anthropicRequestCurrentTurnSteps(req))
	cacheableReplayIdx := lastAnthropicCacheableReplayIndex(replays, req.Tools, anthropicMinimumCacheableTokens(model))
	for idx, replay := range replays {
		replayOptions := anthropicReplayOptions{
			AssistantPreamble: strings.TrimSpace(req.StepReplayData[replay.CallID]),
		}
		if idx == cacheableReplayIdx {
			replayOptions.ResultCacheControl = anthropicPromptCacheControl(promptCacheTTL)
		}
		replayMessages, err := buildAnthropicReplayMessages(replay, req.Tools, replayOptions)
		if err != nil {
			return nil, nil, err
		}
		messages = append(messages, replayMessages...)
	}

	if err := appendPrepared(footer); err != nil {
		return nil, nil, err
	}
	if len(systemBlocks) > 0 && promptCacheTTL != "" {
		systemBlocks[len(systemBlocks)-1].CacheControl = anthropicPromptCacheControl(promptCacheTTL)
	}
	return systemBlocks, messages, nil
}

func splitAnthropicRequestMessages(messages []agent.Message) ([]agent.Message, agent.Message) {
	if len(messages) == 0 {
		return nil, agent.Message{}
	}
	return messages[:len(messages)-1], messages[len(messages)-1]
}

func anthropicCurrentTurnMessages(messages []agent.Message) (agent.Message, agent.Message, agent.Message) {
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

func fallbackAnthropicCurrentTurnMessages(messages []agent.Message) []agent.Message {
	if len(messages) == 0 {
		return nil
	}
	nonSystem := make([]agent.Message, 0, len(messages))
	for _, message := range messages {
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

func anthropicRequestCurrentTurnSteps(req agent.Request) []agent.Step {
	if len(req.CurrentTurnSteps) > 0 || len(req.ConversationTurns) > 0 {
		return req.CurrentTurnSteps
	}
	return req.Steps
}

func historyCacheControlForCurrentUser(cacheControl *anthropicCacheControl, priorTurns []agent.ConversationTurn, historyCacheAssigned bool) *anthropicCacheControl {
	if len(priorTurns) > 0 || historyCacheAssigned {
		return nil
	}
	return cacheControl
}

func appendAnthropicMessage(messages *[]anthropicMessage, message agent.Message, cacheControl *anthropicCacheControl) error {
	if strings.TrimSpace(message.Content) == "" {
		return nil
	}
	switch message.Role {
	case agent.MessageRoleUser, agent.MessageRoleAssistant:
		payload := any(message.Content)
		if cacheControl != nil {
			payload = []any{anthropicRequestTextBlock{
				Type:         "text",
				Text:         message.Content,
				CacheControl: cacheControl,
			}}
		}
		*messages = append(*messages, anthropicMessage{
			Role:    message.Role,
			Content: payload,
		})
		return nil
	default:
		return fmt.Errorf("unsupported anthropic message role %q", message.Role)
	}
}

type anthropicReplayOptions struct {
	ResultCacheControl *anthropicCacheControl
	AssistantPreamble  string
}

func buildAnthropicReplayMessages(replay agent.StepReplay, allowedTools []agent.ToolDefinition, options anthropicReplayOptions) ([]anthropicMessage, error) {
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

	assistantContent, err := anthropicReplayAssistantContent(replay, options.AssistantPreamble)
	if err != nil {
		return nil, err
	}
	assistantContent = append(assistantContent, anthropicRequestToolUseBlock{
		Type:  "tool_use",
		ID:    replay.CallID,
		Name:  def.Name,
		Input: input,
	})
	resultBlock := anthropicRequestToolResultBlock{
		Type:      "tool_result",
		ToolUseID: replay.CallID,
		Content:   replay.Output,
		IsError:   replay.IsError,
	}
	if options.ResultCacheControl != nil {
		resultBlock.CacheControl = options.ResultCacheControl
	}

	return []anthropicMessage{
		{
			Role:    agent.MessageRoleAssistant,
			Content: assistantContent,
		},
		{
			Role:    agent.MessageRoleUser,
			Content: []any{resultBlock},
		},
	}, nil
}

func anthropicReplayAssistantContent(replay agent.StepReplay, rawPreamble string) ([]any, error) {
	rawPreamble = strings.TrimSpace(rawPreamble)
	if rawPreamble == "" {
		assistantContent := make([]any, 0, 1)
		if replay.Thought != "" {
			assistantContent = append(assistantContent, anthropicRequestTextBlock{
				Type: "text",
				Text: replay.Thought,
			})
		}
		return assistantContent, nil
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal([]byte(rawPreamble), &blocks); err != nil {
		return nil, fmt.Errorf("could not decode anthropic replay preamble: %w", err)
	}
	assistantContent := make([]any, 0, len(blocks))
	for _, block := range blocks {
		block = bytes.TrimSpace(block)
		if len(block) == 0 || string(block) == "null" {
			continue
		}
		assistantContent = append(assistantContent, append(json.RawMessage(nil), block...))
	}
	return assistantContent, nil
}

func lastAnthropicCacheableReplayIndex(replays []agent.StepReplay, allowedTools []agent.ToolDefinition, minCacheTokens int) int {
	for idx := len(replays) - 1; idx >= 0; idx-- {
		if anthropicReplayEligibleForCache(replays[idx], allowedTools, minCacheTokens) {
			return idx
		}
	}
	return -1
}

func anthropicReplayEligibleForCache(replay agent.StepReplay, allowedTools []agent.ToolDefinition, minCacheTokens int) bool {
	if minCacheTokens <= 0 {
		minCacheTokens = anthropicReplayCacheMinApproxTokens
	}
	if replay.IsError || anthropicApproxTokenCount(replay.Output) < minCacheTokens {
		return false
	}
	if def, ok := allowedToolDefinition(allowedTools, replay.ToolName); ok {
		return (def.ReadOnly && !def.Mutating) || anthropicKnownReadOnlyReplayTool(replay.ToolName)
	}
	return anthropicKnownReadOnlyReplayTool(replay.ToolName)
}

func anthropicKnownReadOnlyReplayTool(name string) bool {
	switch name {
	case agent.ToolNameListDir,
		agent.ToolNameReadFile,
		agent.ToolNameGrepFiles,
		agent.ToolNameWebSearch,
		agent.ToolNameReadWebPage,
		agent.ToolNameHTTPRequest:
		return true
	default:
		return false
	}
}

func anthropicApproxTokenCount(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	// Anthropic prompt caching uses model-specific minimum token floors. Use a
	// conservative approximation rather than a byte-length heuristic.
	return (utf8.RuneCountInString(text) + 3) / 4
}

func anthropicMinimumCacheableTokens(model string) int {
	model = strings.TrimSpace(model)
	switch {
	case strings.HasPrefix(model, "claude-opus-4-6"),
		strings.HasPrefix(model, "claude-opus-4-5"),
		strings.HasPrefix(model, "claude-haiku-4-5"):
		return 4096
	case strings.HasPrefix(model, "claude-sonnet-4-6"),
		strings.HasPrefix(model, "claude-haiku-3-5"),
		strings.HasPrefix(model, "claude-haiku-3"):
		return 2048
	default:
		return anthropicReplayCacheMinApproxTokens
	}
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
	totalCachedTokens := 0
	for attempt := 0; ; attempt++ {
		response, err := d.completeOnce(ctx, payload)
		if err != nil {
			return agent.Decision{}, err
		}
		totalCachedTokens += response.Usage.CacheReadInputTokens

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
		decision.Usage = agent.Usage{
			CachedTokens: totalCachedTokens,
		}
		return decision, nil
	}
}

func (d *Driver) completeOnce(ctx context.Context, payload messagesRequest) (messagesResponse, error) {
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return messagesResponse{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+"/messages", &body)
	if err != nil {
		return messagesResponse{}, err
	}
	req.Header.Set("x-api-key", d.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("Content-Type", "application/json")
	agent.MaybeWriteDebugPrompt(body.Bytes())

	resp, err := d.client.Do(req)
	if err != nil {
		return messagesResponse{}, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return messagesResponse{}, err
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
		return messagesResponse{}, err
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

func parseMessageDecision(response messagesResponse, allowedTools []agent.ToolDefinition) (agent.Decision, error) {
	text := extractAnthropicText(response.Content)
	toolThought := extractAnthropicToolThought(response.Content)
	finalReasoning := extractAnthropicReasoning(response.Content)
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
		decision.ReplayData = extractAnthropicReplayPreamble(response.Content)
		if toolThought != "" {
			decision.Thought = toolThought
		}
		return decision, nil
	}
	if text != "" {
		decision, err := parseStrictFinalDecision(text)
		if err != nil {
			return agent.Decision{}, err
		}
		decision.Thought = mergeAnthropicThoughts(finalReasoning, decision.Thought)
		return decision, nil
	}
	stopReason := strings.TrimSpace(response.StopReason)
	if stopReason == "" {
		return agent.Decision{}, fmt.Errorf("anthropic response content was empty")
	}
	return agent.Decision{}, fmt.Errorf("anthropic response with stop_reason %q did not include text or tool use", stopReason)
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

func buildAnthropicTools(tools []agent.ToolDefinition, promptCacheTTL string, cacheStaticPrefix bool) []anthropicTool {
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
	if cacheStaticPrefix && len(result) > 0 && promptCacheTTL != "" {
		result[len(result)-1].CacheControl = anthropicPromptCacheControl(promptCacheTTL)
	}
	return result
}

func anthropicPromptCacheControl(ttl string) *anthropicCacheControl {
	ttl = strings.TrimSpace(ttl)
	if ttl == "" {
		return nil
	}
	return &anthropicCacheControl{
		Type: "ephemeral",
		TTL:  ttl,
	}
}

func anthropicToolDescription(tool agent.ToolDefinition) string {
	sections := make([]string, 0, 4)
	if description := strings.TrimSpace(tool.Description); description != "" {
		sections = append(sections, description)
	}
	if format := anthropicToolFormatLabel(tool.CustomFormat); format != "" {
		sections = append(sections, "Input format: "+format+".")
	}
	if notes := strings.TrimSpace(tool.OutputNotes); notes != "" {
		sections = append(sections, "Output notes: "+notes)
	}
	if tags := strings.TrimSpace(strings.Join(tool.SafetyTags, ", ")); tags != "" {
		sections = append(sections, "Safety tags: "+tags+".")
	}
	return strings.Join(sections, "\n\n")
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
	limit := anthropicToolInputExampleLimit(tool)
	if limit == 0 {
		return nil
	}
	examples := make([]map[string]any, 0, min(limit, len(tool.Examples)))
	for _, example := range tool.Examples {
		if payload, ok := anthropicToolInputExample(tool, example); ok {
			examples = append(examples, payload)
			if len(examples) == limit {
				break
			}
		}
	}
	if len(examples) == 0 {
		return nil
	}
	return examples
}

func anthropicToolInputExampleLimit(tool agent.ToolDefinition) int {
	switch tool.Kind {
	case agent.ToolKindFunction, agent.ToolKindCustom:
		return 1
	default:
		return 0
	}
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
		return anthropicCompatibleInputSchema(tool.Parameters)
	}
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"required":             []string{},
		"additionalProperties": false,
	}
}

func anthropicCompatibleInputSchema(schema map[string]any) map[string]any {
	if len(schema) == 0 {
		return schema
	}
	var notes []string
	for _, keyword := range []string{"oneOf", "anyOf", "allOf"} {
		value, ok := schema[keyword]
		if !ok {
			continue
		}
		notes = append(notes, anthropicTopLevelSchemaConstraintNote(keyword, value))
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

func anthropicTopLevelSchemaConstraintNote(keyword string, value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("Anthropic compatibility note: obey the original top-level %s constraint when constructing tool input.", keyword)
	}
	return fmt.Sprintf("Anthropic compatibility note: obey the original top-level %s constraint when constructing tool input: %s", keyword, string(encoded))
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

func extractAnthropicToolThought(blocks []anthropicContentBlock) string {
	var b strings.Builder
	for _, block := range blocks {
		if block.Type == "tool_use" {
			break
		}
		var content string
		switch block.Type {
		case "thinking":
			content = strings.TrimSpace(block.Thinking)
		case "text":
			content = strings.TrimSpace(block.Text)
		default:
			continue
		}
		if content == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(content)
	}
	return b.String()
}

func extractAnthropicReasoning(blocks []anthropicContentBlock) string {
	var b strings.Builder
	for _, block := range blocks {
		if block.Type == "tool_use" {
			break
		}
		if block.Type != "thinking" || strings.TrimSpace(block.Thinking) == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(block.Thinking)
	}
	return b.String()
}

func mergeAnthropicThoughts(parts ...string) string {
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

func extractAnthropicReplayPreamble(blocks []anthropicContentBlock) string {
	if len(blocks) == 0 {
		return ""
	}
	preamble := make([]json.RawMessage, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "tool_use" {
			break
		}
		raw := bytes.TrimSpace(block.rawJSON())
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}
		preamble = append(preamble, raw)
	}
	if len(preamble) == 0 {
		return ""
	}
	return compactJSON(preamble)
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

func extractCustomToolInputPayload(payload map[string]any) (string, error) {
	for _, key := range []string{"patch", "input"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return value, nil
		}
	}
	return "", fmt.Errorf("custom tool input must include non-empty string field \"patch\" or \"input\"")
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

func supportsAnthropicStructuredOutputs(model string) bool {
	model = strings.TrimSpace(model)
	switch {
	case strings.HasPrefix(model, "claude-opus-4-6"),
		strings.HasPrefix(model, "claude-sonnet-4-6"),
		strings.HasPrefix(model, "claude-opus-4-5"),
		strings.HasPrefix(model, "claude-sonnet-4-5"),
		strings.HasPrefix(model, "claude-haiku-4-5"):
		return true
	default:
		return false
	}
}
