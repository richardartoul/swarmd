// See LICENSE for licensing information

// request.go builds Messages API payloads: prompt/message assembly for
// structured and flat requests, step replay reconstruction, and prompt-cache
// placement.

package anthropic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/richardartoul/swarmd/pkg/agent"
)

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

type anthropicRequestImageBlock struct {
	Type   string                      `json:"type"`
	Source anthropicRequestImageSource `json:"source"`
}

type anthropicRequestImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
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
		agent.ToolNameDescribeImage,
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
