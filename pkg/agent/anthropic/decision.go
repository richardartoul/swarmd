// See LICENSE for licensing information

// decision.go parses Messages API responses into agent decisions: content
// blocks, tool use, strict final responses, and thought extraction.

package anthropic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/richardartoul/swarmd/pkg/agent"
)

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
	InputTokens          int `json:"input_tokens"`
	OutputTokens         int `json:"output_tokens"`
	CacheReadInputTokens int `json:"cache_read_input_tokens"`
}

func (u anthropicUsage) toAgentUsage() agent.Usage {
	return agent.Usage{
		InputTokens:  u.InputTokens,
		OutputTokens: u.OutputTokens,
		CachedTokens: u.CacheReadInputTokens,
	}
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
