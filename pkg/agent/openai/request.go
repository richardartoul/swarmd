// See LICENSE for licensing information

// request.go builds Responses API request payloads: instructions, input
// items, and the structured/flat replay and continuation variants used to
// reconstruct multi-step context.

package openai

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/richardartoul/swarmd/pkg/agent"
)

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

func (d *Driver) buildResponsesRequest(req agent.Request, caps openAIAdapterCapabilities) responsesRequest {
	state, _ := decodeOpenAIProviderState(req.ProviderState)
	usePreviousResponseID := shouldUsePreviousResponseID(req, caps, state)
	requestBody := responsesRequest{
		Model:                d.model,
		Instructions:         buildResponsesInstructions(req.Messages, req.Tools, caps),
		Input:                buildResponsesInput(req, caps, state, usePreviousResponseID),
		Text:                 responsesFinalResponseTextConfig(),
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

// buildResponsesInput renders the request conversation as Responses API
// input items from the structured turn view (see [agent.Request]).
func buildResponsesInput(req agent.Request, caps openAIAdapterCapabilities, state openAIProviderState, usePreviousResponseID bool) []responsesInputItem {
	if usePreviousResponseID {
		return buildResponsesContinuationInput(req, caps)
	}
	return buildResponsesReplayInput(req, caps, state)
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
	return buildResponsesToolOutputItem(req, caps, openAIRequestCurrentTurnSteps(req))
}

// buildResponsesToolOutputItem renders the latest executed step as the tool
// output item that answers the provider's pending tool call.
func buildResponsesToolOutputItem(req agent.Request, caps openAIAdapterCapabilities, steps []agent.Step) (responsesInputItem, bool) {
	replay, ok := latestStepReplay(steps)
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
	return responsesInputItem{
		Type:   responsesToolOutputType(toolCall, replay, adapter, adapterOK),
		CallID: toolCall.CallID,
		Output: replay.Output,
	}, true
}

// responsesToolOutputType picks the output item type that matches the wire
// shape of the originating tool call.
func responsesToolOutputType(toolCall responsesOutputItem, replay agent.StepReplay, adapter openAIToolAdapter, adapterOK bool) string {
	if toolCall.Type == "custom_tool_call" {
		return "custom_tool_call_output"
	}
	if adapterOK && openAIReplayBoundaryKind(replay, adapter, true) == agent.ToolBoundaryKindCustom {
		return "custom_tool_call_output"
	}
	return "function_call_output"
}

func latestStepReplay(steps []agent.Step) (agent.StepReplay, bool) {
	replays := agent.BuildStepReplays(steps)
	if len(replays) == 0 {
		return agent.StepReplay{}, false
	}
	return replays[len(replays)-1], true
}

func openAICurrentTurnMessages(req agent.Request) []agent.Message {
	return append([]agent.Message(nil), req.CurrentTurnMessages...)
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
	return req.CurrentTurnSteps
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

func responsesFinalResponseTextConfig() *responsesTextConfig {
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
