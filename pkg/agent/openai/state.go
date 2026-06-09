// See LICENSE for licensing information

// state.go encodes and decodes the opaque provider state and per-step
// replay payloads carried between driver requests.

package openai

import (
	"bytes"
	"encoding/json"
	"strings"
)

type openAIProviderState struct {
	ResponseID string                `json:"response_id,omitempty"`
	Output     []responsesOutputItem `json:"output,omitempty"`
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
