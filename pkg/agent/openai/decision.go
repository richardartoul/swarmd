// See LICENSE for licensing information

// decision.go parses Responses API output into agent decisions: tool calls,
// strict final responses, and the reasoning/thought/refusal extractors.

package openai

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/richardartoul/swarmd/pkg/agent"
)

type responsesUsage struct {
	InputTokens        int                         `json:"input_tokens"`
	OutputTokens       int                         `json:"output_tokens"`
	InputTokensDetails responsesInputTokensDetails `json:"input_tokens_details"`
}

type responsesInputTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

func (u responsesUsage) toAgentUsage() agent.Usage {
	return agent.Usage{
		InputTokens:  u.InputTokens,
		OutputTokens: u.OutputTokens,
		CachedTokens: u.InputTokensDetails.CachedTokens,
	}
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

type openAIFunctionCall struct {
	Name      string
	Arguments string
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

func latestResponsesToolCall(items []responsesOutputItem) (responsesOutputItem, bool) {
	for idx := len(items) - 1; idx >= 0; idx-- {
		switch items[idx].Type {
		case "function_call", "custom_tool_call":
			return items[idx], true
		}
	}
	return responsesOutputItem{}, false
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
