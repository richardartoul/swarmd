package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/richardartoul/swarmd/pkg/agent"
	"github.com/richardartoul/swarmd/pkg/sh/interp"
)

func testCurrentStateMessage(step int) string {
	startedAt := time.Date(2026, time.March, 18, 15, 4, 5, 0, time.UTC)
	return strings.Join([]string{
		"Current execution state",
		fmt.Sprintf("Current step number: %d", step),
		"Current time at start of this run: " + startedAt.Format(time.RFC3339),
		fmt.Sprintf("Current Unix time at start of this run: %d", startedAt.Unix()),
	}, "\n")
}

func TestNewRequiresAPIKeyAndModel(t *testing.T) {
	t.Parallel()

	if _, err := New(Config{Model: "gpt-test"}); err == nil {
		t.Fatal("New() error = nil, want missing api key error")
	}
	if _, err := New(Config{APIKey: "test-key"}); err == nil {
		t.Fatal("New() error = nil, want missing model error")
	}
}

func TestNewParsesReasoningEffortFromModelSuffix(t *testing.T) {
	t.Parallel()

	driver, err := New(Config{
		APIKey: "test-key",
		Model:  "gpt-5.4-xhigh",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if driver.model != "gpt-5.4" {
		t.Fatalf("driver.model = %q, want %q", driver.model, "gpt-5.4")
	}
	if driver.reasoningEffort != "high" {
		t.Fatalf("driver.reasoningEffort = %q, want %q", driver.reasoningEffort, "high")
	}
}

func TestNewRejectsAgentOwnedPromptSettings(t *testing.T) {
	t.Parallel()

	if _, err := New(Config{
		APIKey:       "test-key",
		Model:        "gpt-5.4",
		SystemPrompt: "test prompt",
	}); err == nil {
		t.Fatal("New() error = nil, want system prompt rejection")
	}
}

func TestNewDefaultsHTTPTimeoutToFiveMinutes(t *testing.T) {
	t.Parallel()

	driver, err := New(Config{
		APIKey: "test-key",
		Model:  "gpt-5.4",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if driver.client.Timeout != 5*time.Minute {
		t.Fatalf("driver.client.Timeout = %v, want %v", driver.client.Timeout, 5*time.Minute)
	}
}

func TestDriverNextPreservesWrappedTimeoutErrors(t *testing.T) {
	t.Parallel()

	driver, err := New(Config{
		APIKey:  "test-key",
		BaseURL: "https://example.test",
		Model:   "gpt-5.4",
		HTTPClient: &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return nil, &url.Error{Op: req.Method, URL: req.URL.String(), Err: context.DeadlineExceeded}
		})},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = driver.Next(context.Background(), agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleUser, Content: "say hello"},
		},
	})
	if err == nil {
		t.Fatal("driver.Next() error = nil, want timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("driver.Next() error = %v, want wrapped deadline exceeded", err)
	}
	if !strings.Contains(err.Error(), "send openai request") {
		t.Fatalf("driver.Next() error = %q, want wrapped send context", err.Error())
	}
}

func TestNewRejectsInvalidPromptCacheRetention(t *testing.T) {
	t.Parallel()

	if _, err := New(Config{
		APIKey:               "test-key",
		Model:                "gpt-5.4",
		PromptCacheRetention: "forever",
	}); err == nil {
		t.Fatal("New() error = nil, want invalid prompt cache retention rejection")
	}
}

func TestDriverNextParsesStrictFinalDecision(t *testing.T) {
	t.Parallel()

	server, requests := newResponsesTestServer(t, []responsesTestServerResponse{
		{OutputText: agent.StrictFinalResponseExample("inspect", "done")},
	})
	defer server.Close()

	driver := newTestDriver(t, server.URL)
	decision, err := driver.Next(context.Background(), agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleSystem, Content: "test prompt"},
			{Role: agent.MessageRoleUser, Content: "list the current directory"},
		},
	})
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if decision.Finish == nil {
		t.Fatal("decision.Finish = nil, want finish decision")
	}
	if decision.Thought != "inspect" {
		t.Fatalf("decision.Thought = %q, want %q", decision.Thought, "inspect")
	}
	if got := decision.Finish.Value; got != "done" {
		t.Fatalf("decision.Finish.Value = %#v, want %q", got, "done")
	}
	if decision.Usage.CachedTokens != 0 {
		t.Fatalf("decision.Usage.CachedTokens = %d, want 0", decision.Usage.CachedTokens)
	}

	if len(requests.snapshot()) != 1 {
		t.Fatalf("len(requests) = %d, want 1", len(requests.snapshot()))
	}
	got := requests.snapshot()[0]
	if got.Model != "gpt-5.4" {
		t.Fatalf("request model = %q, want %q", got.Model, "gpt-5.4")
	}
	if got.Reasoning == nil {
		t.Fatal("request reasoning = nil, want summary config")
	}
	if got.Reasoning.Effort != "" {
		t.Fatalf("request reasoning effort = %q, want empty by default", got.Reasoning.Effort)
	}
	if got.Reasoning.Summary != "auto" {
		t.Fatalf("request reasoning summary = %q, want %q", got.Reasoning.Summary, "auto")
	}
	if got.ToolChoice != "" {
		t.Fatalf("request tool_choice = %q, want empty without tools", got.ToolChoice)
	}
	if got.ParallelToolCalls != nil {
		t.Fatalf("request parallel_tool_calls = %#v, want nil without tools", got.ParallelToolCalls)
	}
	if got.Instructions != "test prompt" {
		t.Fatalf("request instructions = %q, want %q", got.Instructions, "test prompt")
	}
	if len(got.Input) != 1 {
		t.Fatalf("len(request input) = %d, want 1", len(got.Input))
	}
	if got.Input[0].Role != agent.MessageRoleUser || got.Input[0].Content != "list the current directory" {
		t.Fatalf("first request input = %#v, want user prompt", got.Input[0])
	}
}

func TestDriverNextSendsReasoningEffortFromModelSuffix(t *testing.T) {
	t.Parallel()

	server, requests := newResponsesTestServer(t, []responsesTestServerResponse{
		{OutputText: agent.StrictFinalResponseExample("inspect", "done")},
	})
	defer server.Close()

	driver, err := New(Config{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Model:   "gpt-5.4-xnone",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = driver.Next(context.Background(), agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleUser, Content: "say hello"},
		},
	})
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}

	snapshot := requests.snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("len(requests) = %d, want 1", len(snapshot))
	}
	if snapshot[0].Model != "gpt-5.4" {
		t.Fatalf("request model = %q, want %q", snapshot[0].Model, "gpt-5.4")
	}
	if snapshot[0].Reasoning == nil {
		t.Fatal("request reasoning = nil, want none effort object")
	}
	if snapshot[0].Reasoning.Effort != "none" {
		t.Fatalf("request reasoning effort = %q, want %q", snapshot[0].Reasoning.Effort, "none")
	}
	if snapshot[0].Reasoning.Summary != "auto" {
		t.Fatalf("request reasoning summary = %q, want %q", snapshot[0].Reasoning.Summary, "auto")
	}
}

func TestDriverNextCapturesCachedTokens(t *testing.T) {
	t.Parallel()

	server, _ := newResponsesTestServer(t, []responsesTestServerResponse{
		{
			OutputText:   agent.StrictFinalResponseExample("inspect", "done"),
			CachedTokens: 1536,
		},
	})
	defer server.Close()

	driver := newTestDriver(t, server.URL)
	decision, err := driver.Next(context.Background(), agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleUser, Content: "list the current directory"},
		},
	})
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if decision.Usage.CachedTokens != 1536 {
		t.Fatalf("decision.Usage.CachedTokens = %d, want %d", decision.Usage.CachedTokens, 1536)
	}
}

func TestBuildResponsesRequestAddsStructuredTextFormatOnSupportedNoToolModel(t *testing.T) {
	t.Parallel()

	driver := &Driver{
		model:   "gpt-5.4",
		baseURL: DefaultBaseURL,
	}
	request := driver.buildResponsesRequest(agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleUser, Content: "list the current directory"},
		},
	}, openAIAdapterCapabilities{SupportsCustomTools: true})
	if request.Text == nil || request.Text.Format == nil {
		t.Fatal("request text.format = nil, want structured output config")
	}
	if request.Text.Format.Type != "json_schema" {
		t.Fatalf("request text.format.type = %q, want %q", request.Text.Format.Type, "json_schema")
	}
	if request.Text.Format.Name != "agent_final_response" {
		t.Fatalf("request text.format.name = %q, want %q", request.Text.Format.Name, "agent_final_response")
	}
	if !request.Text.Format.Strict {
		t.Fatal("request text.format.strict = false, want true")
	}
	if request.Reasoning == nil {
		t.Fatal("request reasoning = nil, want summary config")
	}
	if request.Reasoning.Summary != "auto" {
		t.Fatalf("request reasoning summary = %q, want %q", request.Reasoning.Summary, "auto")
	}
	properties, ok := request.Text.Format.Schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("request text.format.schema.properties = %#v, want object", request.Text.Format.Schema["properties"])
	}
	thoughtSchema, ok := properties["thought"].(map[string]any)
	if !ok {
		t.Fatalf("request text.format.schema.properties[thought] = %#v, want object", properties["thought"])
	}
	resultSchema, ok := properties["result_json"].(map[string]any)
	if !ok {
		t.Fatalf("request text.format.schema.properties[result_json] = %#v, want object", properties["result_json"])
	}
	required, ok := request.Text.Format.Schema["required"].([]string)
	if !ok {
		t.Fatalf("request text.format.schema.required = %#v, want []string", request.Text.Format.Schema["required"])
	}
	if len(required) != 2 || required[0] != "thought" || required[1] != "result_json" {
		t.Fatalf("request text.format schema required = %#v, want [thought result_json]", required)
	}
	if thoughtSchema["type"] != "string" {
		t.Fatalf("request text.format.schema.properties[thought][type] = %#v, want %q", thoughtSchema["type"], "string")
	}
	if resultSchema["type"] != "string" {
		t.Fatalf("request text.format.schema.properties[result_json][type] = %#v, want %q", resultSchema["type"], "string")
	}
}

func TestBuildResponsesRequestLeavesReasoningNilForUnsupportedSummaryModel(t *testing.T) {
	t.Parallel()

	driver := &Driver{
		model:   "gpt-4.1",
		baseURL: DefaultBaseURL,
	}
	request := driver.buildResponsesRequest(agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleUser, Content: "list the current directory"},
		},
	}, openAIAdapterCapabilities{SupportsCustomTools: true})
	if request.Reasoning != nil {
		t.Fatalf("request reasoning = %#v, want nil for unsupported summary model", request.Reasoning)
	}
}

func TestBuildResponsesRequestAddsStructuredTextFormatWhenToolsAvailable(t *testing.T) {
	t.Parallel()

	driver := &Driver{
		model:   "gpt-5.4",
		baseURL: DefaultBaseURL,
	}
	request := driver.buildResponsesRequest(agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleUser, Content: "say done"},
		},
		Tools: []agent.ToolDefinition{{
			Name:        agent.ToolNameReadFile,
			Description: "Reads a file.",
			Kind:        agent.ToolKindFunction,
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false},
		}},
	}, openAIAdapterCapabilities{SupportsCustomTools: true})
	if request.Text == nil || request.Text.Format == nil {
		t.Fatal("request text.format = nil, want structured output config")
	}
	if request.Text.Format.Name != "agent_final_response" {
		t.Fatalf("request text.format.name = %q, want %q", request.Text.Format.Name, "agent_final_response")
	}
	required, ok := request.Text.Format.Schema["required"].([]string)
	if !ok {
		t.Fatalf("request text.format.schema.required = %#v, want []string", request.Text.Format.Schema["required"])
	}
	if len(required) != 2 || required[0] != "thought" || required[1] != "result_json" {
		t.Fatalf("request text.format schema required = %#v, want [thought result_json]", required)
	}
	if request.Reasoning == nil {
		t.Fatal("request reasoning = nil, want summary config")
	}
	if request.Reasoning.Summary != "auto" {
		t.Fatalf("request reasoning summary = %q, want %q", request.Reasoning.Summary, "auto")
	}
	properties, ok := request.Text.Format.Schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("request text.format.schema.properties = %#v, want object", request.Text.Format.Schema["properties"])
	}
	thoughtSchema, ok := properties["thought"].(map[string]any)
	if !ok {
		t.Fatalf("request text.format.schema.properties[thought] = %#v, want object", properties["thought"])
	}
	resultSchema, ok := properties["result_json"].(map[string]any)
	if !ok {
		t.Fatalf("request text.format.schema.properties[result_json] = %#v, want object", properties["result_json"])
	}
	if thoughtSchema["type"] != "string" {
		t.Fatalf("request text.format.schema.properties[thought][type] = %#v, want %q", thoughtSchema["type"], "string")
	}
	if resultSchema["type"] != "string" {
		t.Fatalf("request text.format.schema.properties[result_json][type] = %#v, want %q", resultSchema["type"], "string")
	}
}

func TestNewRejectsUnsupportedModelForStructuredOutputs(t *testing.T) {
	t.Parallel()

	if _, err := New(Config{
		APIKey: "test-key",
		Model:  "gpt-test",
	}); err == nil {
		t.Fatal("New() error = nil, want unsupported model rejection")
	}
}

func TestBuildResponsesRequestUsesStructuredTextFormatForCustomBaseURL(t *testing.T) {
	t.Parallel()

	driver := &Driver{
		model:   "gpt-5.4",
		baseURL: "https://compatible.example/v1",
	}
	request := driver.buildResponsesRequest(agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleUser, Content: "say done"},
		},
	}, openAIAdapterCapabilities{})
	if request.Text == nil || request.Text.Format == nil {
		t.Fatalf("request text = %#v, want structured output for supported models regardless of base URL", request.Text)
	}
}

func TestDriverNextIgnoresTrailingJSONObjects(t *testing.T) {
	t.Parallel()

	server, _ := newResponsesTestServer(t, []responsesTestServerResponse{
		{
			OutputText: agent.StrictFinalResponseExample("inspect", "done") + "\n" +
				agent.StrictFinalResponseExample("ignored", "later"),
		},
	})
	defer server.Close()

	driver := newTestDriver(t, server.URL)
	_, err := driver.Next(context.Background(), agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleUser, Content: "inspect the box"},
		},
	})
	if err == nil {
		t.Fatal("Next() error = nil, want strict final parser rejection")
	}
}

func TestDriverNextMovesSystemMessagesIntoInstructions(t *testing.T) {
	t.Parallel()

	server, requests := newResponsesTestServer(t, []responsesTestServerResponse{
		{OutputText: agent.StrictFinalResponseExample("follow up", "done")},
	})
	defer server.Close()

	driver := newTestDriver(t, server.URL)
	_, err := driver.Next(context.Background(), agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleSystem, Content: "test prompt"},
			{Role: agent.MessageRoleSystem, Content: "runtime-only guidance"},
			{Role: agent.MessageRoleUser, Content: "show it to me"},
		},
	})
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}

	snapshot := requests.snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("len(requests) = %d, want 1", len(snapshot))
	}
	got := snapshot[0]
	if got.Instructions != "test prompt\n\nruntime-only guidance" {
		t.Fatalf("request instructions = %q, want combined system messages", got.Instructions)
	}
	if len(got.Input) != 1 {
		t.Fatalf("len(request input) = %d, want 1", len(got.Input))
	}
	if got.Input[0].Role != agent.MessageRoleUser || got.Input[0].Content != "show it to me" {
		t.Fatalf("first request input = %#v, want user message only", got.Input[0])
	}
}

func TestBuildResponsesInputReplaysNativeToolHistory(t *testing.T) {
	t.Parallel()

	input := buildResponsesInput(agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleSystem, Content: "test prompt"},
			{Role: agent.MessageRoleUser, Content: "trigger context"},
			{Role: agent.MessageRoleUser, Content: testCurrentStateMessage(2)},
		},
		Steps: []agent.Step{
			{
				Index:          1,
				Thought:        "inspect the file first",
				ActionName:     agent.ToolNameReadFile,
				ActionToolKind: agent.ToolKindFunction,
				ActionInput:    `{"file_path":"/tmp/demo.txt"}`,
				Status:         agent.StepStatusOK,
				CWDAfter:       "/workspace",
				ActionOutput:   "demo content",
			},
			{
				Index:          2,
				ActionName:     agent.ToolNameApplyPatch,
				ActionToolKind: agent.ToolKindCustom,
				ActionInput:    "*** Begin Patch\n*** End Patch",
				Status:         agent.StepStatusOK,
				CWDAfter:       "/workspace",
			},
		},
		Tools: []agent.ToolDefinition{
			{
				Name:        agent.ToolNameReadFile,
				Kind:        agent.ToolKindFunction,
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{"file_path": map[string]any{"type": "string"}}, "required": []string{"file_path"}, "additionalProperties": false},
				Description: "Reads a file.",
			},
			{
				Name:         agent.ToolNameApplyPatch,
				Kind:         agent.ToolKindCustom,
				CustomFormat: &agent.ToolFormat{Type: "grammar", Syntax: "lark", Definition: "start: PATCH\nPATCH: /.+/s"},
				Interop: agent.ToolInterop{
					OpenAIPreferredKind: agent.ToolBoundaryKindCustom,
					OpenAIFallbackKind:  agent.ToolBoundaryKindFunction,
				},
			},
		},
	}, openAIAdapterCapabilities{SupportsCustomTools: true}, openAIProviderState{}, false)

	if len(input) != 7 {
		t.Fatalf("len(input) = %d, want 7", len(input))
	}
	if input[0].Role != agent.MessageRoleUser || input[0].Content != "trigger context" {
		t.Fatalf("input[0] = %#v, want trigger context first", input[0])
	}
	if input[1].Role != agent.MessageRoleAssistant || input[1].Content != "inspect the file first" {
		t.Fatalf("input[1] = %#v, want assistant thought message", input[1])
	}
	if input[2].Type != "function_call" || input[2].CallID != "step_1" || input[2].Name != agent.ToolNameReadFile {
		t.Fatalf("input[2] = %#v, want function_call replay", input[2])
	}
	if input[2].Arguments != `{"file_path":"/tmp/demo.txt"}` {
		t.Fatalf("input[2].Arguments = %q, want replayed function arguments", input[2].Arguments)
	}
	if input[3].Type != "function_call_output" || input[3].CallID != "step_1" {
		t.Fatalf("input[3] = %#v, want function_call_output replay", input[3])
	}
	if !strings.Contains(input[3].Output, "demo content") {
		t.Fatalf("input[3].Output = %q, want observation output", input[3].Output)
	}
	if input[4].Type != "custom_tool_call" || input[4].CallID != "step_2" || input[4].Name != agent.ToolNameApplyPatch {
		t.Fatalf("input[4] = %#v, want custom_tool_call replay", input[4])
	}
	if input[4].Input != "*** Begin Patch\n*** End Patch" {
		t.Fatalf("input[4].Input = %q, want raw custom tool input", input[4].Input)
	}
	if input[5].Type != "custom_tool_call_output" || input[5].CallID != "step_2" {
		t.Fatalf("input[5] = %#v, want custom_tool_call_output replay", input[5])
	}
	if input[6].Content != testCurrentStateMessage(2) {
		t.Fatalf("input[6].Content = %q, want current-state footer last", input[6].Content)
	}
}

func TestBuildResponsesInputUsesRawReplayDataWhenAvailable(t *testing.T) {
	t.Parallel()

	input := buildResponsesInput(agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleSystem, Content: "test prompt"},
			{Role: agent.MessageRoleUser, Content: "trigger context"},
			{Role: agent.MessageRoleUser, Content: testCurrentStateMessage(2)},
		},
		Steps: []agent.Step{{
			Index:          1,
			Thought:        "inspect the file first",
			ActionName:     agent.ToolNameReadFile,
			ActionToolKind: agent.ToolKindFunction,
			ActionInput:    `{"file_path":"/tmp/demo.txt"}`,
			Status:         agent.StepStatusOK,
			CWDAfter:       "/workspace",
			ActionOutput:   "demo content",
		}},
		Tools: []agent.ToolDefinition{{
			Name:       agent.ToolNameReadFile,
			Kind:       agent.ToolKindFunction,
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"file_path": map[string]any{"type": "string"}}, "required": []string{"file_path"}, "additionalProperties": false},
		}},
		StepReplayData: map[string]string{
			"step_1": compactJSON([]responsesOutputItem{
				{
					Type: "reasoning",
					Summary: []responsesOutputContent{{
						Type: "summary_text",
						Text: "inspect the file first",
					}},
				},
				{
					Type:      "function_call",
					CallID:    "call_abc",
					Name:      agent.ToolNameReadFile,
					Arguments: `{"file_path":"/tmp/demo.txt"}`,
				},
			}),
		},
	}, openAIAdapterCapabilities{SupportsCustomTools: true}, openAIProviderState{}, false)

	if len(input) != 5 {
		t.Fatalf("len(input) = %d, want 5", len(input))
	}
	if input[0].Role != agent.MessageRoleUser || input[0].Content != "trigger context" {
		t.Fatalf("input[0] = %#v, want trigger context first", input[0])
	}
	if input[1].Type != "reasoning" || len(input[1].Summary) != 1 || input[1].Summary[0].Text != "inspect the file first" {
		t.Fatalf("input[1] = %#v, want raw reasoning replay", input[1])
	}
	if input[2].Type != "function_call" || input[2].CallID != "call_abc" || input[2].Name != agent.ToolNameReadFile {
		t.Fatalf("input[2] = %#v, want raw function_call replay", input[2])
	}
	if input[3].Type != "function_call_output" || input[3].CallID != "call_abc" {
		t.Fatalf("input[3] = %#v, want function_call_output replay with provider call id", input[3])
	}
	if !strings.Contains(input[3].Output, "demo content") {
		t.Fatalf("input[3].Output = %q, want observation output", input[3].Output)
	}
	if input[4].Content != testCurrentStateMessage(2) {
		t.Fatalf("input[4].Content = %q, want current-state footer last", input[4].Content)
	}
}

func TestBuildResponsesInputInterleavesPriorSessionTurnBeforeCurrentTurn(t *testing.T) {
	t.Parallel()

	input := buildResponsesInput(agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleSystem, Content: "test prompt"},
		},
		ConversationTurns: []agent.ConversationTurn{{
			User: agent.Message{
				Role:    agent.MessageRoleUser,
				Content: "first question",
			},
			Assistant: &agent.Message{
				Role:    agent.MessageRoleAssistant,
				Content: "first answer",
			},
			Steps: []agent.Step{{
				Index:          1,
				Thought:        "inspect the file first",
				ActionName:     agent.ToolNameReadFile,
				ActionToolKind: agent.ToolKindFunction,
				ActionInput:    `{"file_path":"/tmp/demo.txt"}`,
				Status:         agent.StepStatusOK,
				CWDAfter:       "/workspace",
				ActionOutput:   "demo content",
			}},
		}},
		CurrentTurnMessages: []agent.Message{
			{Role: agent.MessageRoleUser, Content: "second question"},
			{Role: agent.MessageRoleUser, Content: "stable protocol"},
			{Role: agent.MessageRoleUser, Content: testCurrentStateMessage(1)},
		},
		Tools: []agent.ToolDefinition{{
			Name:       agent.ToolNameReadFile,
			Kind:       agent.ToolKindFunction,
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"file_path": map[string]any{"type": "string"}}, "required": []string{"file_path"}, "additionalProperties": false},
		}},
	}, openAIAdapterCapabilities{SupportsCustomTools: true}, openAIProviderState{}, false)

	if len(input) != 8 {
		t.Fatalf("len(input) = %d, want 8", len(input))
	}
	if input[0].Role != agent.MessageRoleUser || input[0].Content != "first question" {
		t.Fatalf("input[0] = %#v, want prior turn user first", input[0])
	}
	if input[1].Role != agent.MessageRoleAssistant || input[1].Content != "inspect the file first" {
		t.Fatalf("input[1] = %#v, want replayed assistant thought", input[1])
	}
	if input[2].Type != "function_call" || input[2].CallID != "step_1" {
		t.Fatalf("input[2] = %#v, want replayed function call", input[2])
	}
	if input[3].Type != "function_call_output" || input[3].CallID != "step_1" {
		t.Fatalf("input[3] = %#v, want replayed function call output", input[3])
	}
	if input[4].Role != agent.MessageRoleAssistant || input[4].Content != "first answer" {
		t.Fatalf("input[4] = %#v, want prior turn assistant before current turn", input[4])
	}
	if input[5].Role != agent.MessageRoleUser || input[5].Content != "second question" {
		t.Fatalf("input[5] = %#v, want current turn user after prior turn history", input[5])
	}
	if input[6].Role != agent.MessageRoleUser || input[6].Content != "stable protocol" {
		t.Fatalf("input[6] = %#v, want protocol before the current-state footer", input[6])
	}
	if input[7].Content != testCurrentStateMessage(1) {
		t.Fatalf("input[7] = %#v, want current-state footer last", input[7])
	}
}

func TestBuildResponsesRequestUsesPreviousResponseIDForToolContinuation(t *testing.T) {
	t.Parallel()

	driver := &Driver{
		model:   "gpt-5.4",
		baseURL: DefaultBaseURL,
	}
	request := driver.buildResponsesRequest(agent.Request{
		Step: 2,
		Messages: []agent.Message{
			{Role: agent.MessageRoleSystem, Content: "test prompt"},
			{Role: agent.MessageRoleUser, Content: "trigger context"},
			{Role: agent.MessageRoleUser, Content: "stable protocol"},
			{Role: agent.MessageRoleUser, Content: testCurrentStateMessage(2)},
		},
		Steps: []agent.Step{{
			Index:          1,
			ActionName:     agent.ToolNameReadFile,
			ActionToolKind: agent.ToolKindFunction,
			ActionInput:    `{"file_path":"/tmp/demo.txt"}`,
			Status:         agent.StepStatusOK,
			CWDAfter:       "/workspace",
			ActionOutput:   "demo content",
		}},
		Tools: []agent.ToolDefinition{{
			Name:       agent.ToolNameReadFile,
			Kind:       agent.ToolKindFunction,
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"file_path": map[string]any{"type": "string"}}, "required": []string{"file_path"}, "additionalProperties": false},
		}},
		StepReplayData: map[string]string{
			"step_1": compactJSON([]responsesOutputItem{{
				Type:      "function_call",
				CallID:    "call_abc",
				Name:      agent.ToolNameReadFile,
				Arguments: `{"file_path":"/tmp/demo.txt"}`,
			}}),
		},
		ProviderState: compactJSON(openAIProviderState{
			ResponseID: "resp_1",
		}),
	}, openAIAdapterCapabilities{SupportsCustomTools: true})

	if request.PreviousResponseID != "resp_1" {
		t.Fatalf("request previous_response_id = %q, want %q", request.PreviousResponseID, "resp_1")
	}
	if len(request.Input) != 2 {
		t.Fatalf("len(request.Input) = %d, want 2", len(request.Input))
	}
	if request.Input[0].Type != "function_call_output" || request.Input[0].CallID != "call_abc" {
		t.Fatalf("request.Input[0] = %#v, want function_call_output with provider call id", request.Input[0])
	}
	if !strings.Contains(request.Input[0].Output, "demo content") {
		t.Fatalf("request.Input[0].Output = %q, want observation output", request.Input[0].Output)
	}
	if request.Input[1].Content != testCurrentStateMessage(2) {
		t.Fatalf("request.Input[1].Content = %q, want current-state footer", request.Input[1].Content)
	}
}

func TestBuildResponsesRequestUsesPreviousResponseIDForResolvedNewTurn(t *testing.T) {
	t.Parallel()

	driver := &Driver{
		model:   "gpt-5.4",
		baseURL: DefaultBaseURL,
	}
	request := driver.buildResponsesRequest(agent.Request{
		Step: 1,
		Messages: []agent.Message{
			{Role: agent.MessageRoleSystem, Content: "test prompt"},
		},
		CurrentTurnMessages: []agent.Message{
			{Role: agent.MessageRoleUser, Content: "second question"},
			{Role: agent.MessageRoleUser, Content: "stable protocol"},
			{Role: agent.MessageRoleUser, Content: testCurrentStateMessage(1)},
		},
		ProviderState: compactJSON(openAIProviderState{
			ResponseID: "resp_1",
			Output: []responsesOutputItem{{
				Type: "message",
				Role: agent.MessageRoleAssistant,
			}},
		}),
	}, openAIAdapterCapabilities{SupportsCustomTools: true})

	if request.PreviousResponseID != "resp_1" {
		t.Fatalf("request previous_response_id = %q, want %q", request.PreviousResponseID, "resp_1")
	}
	if len(request.Input) != 2 {
		t.Fatalf("len(request.Input) = %d, want 2", len(request.Input))
	}
	if request.Input[0].Role != agent.MessageRoleUser || request.Input[0].Content != "second question" {
		t.Fatalf("request.Input[0] = %#v, want current user turn", request.Input[0])
	}
	if request.Input[1].Content != testCurrentStateMessage(1) {
		t.Fatalf("request.Input[1] = %#v, want current-state footer", request.Input[1])
	}
}

func TestBuildResponsesRequestFallsBackToReplayForNewTurnAfterUnresolvedToolCall(t *testing.T) {
	t.Parallel()

	driver := &Driver{
		model:   "gpt-5.4",
		baseURL: DefaultBaseURL,
	}
	request := driver.buildResponsesRequest(agent.Request{
		Step: 1,
		Messages: []agent.Message{
			{Role: agent.MessageRoleSystem, Content: "test prompt"},
		},
		ConversationTurns: []agent.ConversationTurn{{
			User: agent.Message{
				Role:    agent.MessageRoleUser,
				Content: "first question",
			},
			Steps: []agent.Step{{
				Index:          1,
				ActionName:     agent.ToolNameReadFile,
				ActionToolKind: agent.ToolKindFunction,
				ActionInput:    `{"file_path":"/tmp/demo.txt"}`,
				Status:         agent.StepStatusOK,
				CWDAfter:       "/workspace",
				ActionOutput:   "demo content",
			}},
		}},
		CurrentTurnMessages: []agent.Message{
			{Role: agent.MessageRoleUser, Content: "second question"},
			{Role: agent.MessageRoleUser, Content: "stable protocol"},
			{Role: agent.MessageRoleUser, Content: testCurrentStateMessage(1)},
		},
		Tools: []agent.ToolDefinition{{
			Name:       agent.ToolNameReadFile,
			Kind:       agent.ToolKindFunction,
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"file_path": map[string]any{"type": "string"}}, "required": []string{"file_path"}, "additionalProperties": false},
		}},
		ProviderState: compactJSON(openAIProviderState{
			ResponseID: "resp_1",
			Output: []responsesOutputItem{{
				Type:      "function_call",
				CallID:    "call_abc",
				Name:      agent.ToolNameReadFile,
				Arguments: `{"file_path":"/tmp/demo.txt"}`,
			}},
		}),
	}, openAIAdapterCapabilities{SupportsCustomTools: true})

	if request.PreviousResponseID != "" {
		t.Fatalf("request previous_response_id = %q, want empty for unresolved prior tool call", request.PreviousResponseID)
	}
	if len(request.Input) != 6 {
		t.Fatalf("len(request.Input) = %d, want 6", len(request.Input))
	}
	if request.Input[0].Role != agent.MessageRoleUser || request.Input[0].Content != "first question" {
		t.Fatalf("request.Input[0] = %#v, want prior turn user first", request.Input[0])
	}
	if request.Input[1].Type != "function_call" || request.Input[1].CallID != "step_1" {
		t.Fatalf("request.Input[1] = %#v, want replayed function_call", request.Input[1])
	}
	if request.Input[2].Type != "function_call_output" || request.Input[2].CallID != "step_1" {
		t.Fatalf("request.Input[2] = %#v, want replayed function_call_output", request.Input[2])
	}
	if request.Input[3].Role != agent.MessageRoleUser || request.Input[3].Content != "second question" {
		t.Fatalf("request.Input[3] = %#v, want new turn user after replayed history", request.Input[3])
	}
	if request.Input[4].Role != agent.MessageRoleUser || request.Input[4].Content != "stable protocol" {
		t.Fatalf("request.Input[4] = %#v, want protocol before current-state footer", request.Input[4])
	}
	if request.Input[5].Content != testCurrentStateMessage(1) {
		t.Fatalf("request.Input[5] = %#v, want current-state footer", request.Input[5])
	}
}

func TestParseResponsesDecisionCapturesReplayAndProviderStateForToolCalls(t *testing.T) {
	t.Parallel()

	decision, err := parseResponsesDecision(responsesResponse{
		ID: "resp_1",
		Output: []responsesOutputItem{
			{
				Type: "reasoning",
				Summary: []responsesOutputContent{{
					Type: "summary_text",
					Text: "inspect the file first",
				}},
			},
			{
				Type:      "function_call",
				CallID:    "call_abc",
				Name:      agent.ToolNameReadFile,
				Arguments: `{"file_path":"/tmp/demo.txt"}`,
			},
		},
	}, []agent.ToolDefinition{{
		Name: agent.ToolNameReadFile,
		Kind: agent.ToolKindFunction,
	}}, openAIAdapterCapabilities{})
	if err != nil {
		t.Fatalf("parseResponsesDecision() error = %v", err)
	}
	if decision.Tool == nil || decision.Tool.Name != agent.ToolNameReadFile {
		t.Fatalf("decision.Tool = %#v, want read_file", decision.Tool)
	}
	if decision.ReplayData == "" {
		t.Fatal("decision.ReplayData = empty, want raw replay output")
	}
	replayItems, ok := decodeOpenAIReplayOutputItems(decision.ReplayData)
	if !ok || len(replayItems) != 2 {
		t.Fatalf("decision.ReplayData = %q, want two replay items", decision.ReplayData)
	}
	if replayItems[1].CallID != "call_abc" {
		t.Fatalf("replayItems[1].CallID = %q, want %q", replayItems[1].CallID, "call_abc")
	}
	if decision.ProviderState == "" {
		t.Fatal("decision.ProviderState = empty, want provider continuation state")
	}
	state, ok := decodeOpenAIProviderState(decision.ProviderState)
	if !ok {
		t.Fatalf("decision.ProviderState = %q, want decodable provider state", decision.ProviderState)
	}
	if state.ResponseID != "resp_1" {
		t.Fatalf("provider state response_id = %q, want %q", state.ResponseID, "resp_1")
	}
	if len(state.Output) != 2 {
		t.Fatalf("len(provider state output) = %d, want 2", len(state.Output))
	}
}

func TestBuildResponsesToolsMovesUniqueHintsIntoDescriptions(t *testing.T) {
	t.Parallel()

	tools := buildResponsesTools([]agent.ToolDefinition{{
		Name:        agent.ToolNameReadFile,
		Description: "Reads a file.",
		Kind:        agent.ToolKindFunction,
		OutputNotes: "Returns bounded slices with truncation markers when needed.",
		SafetyTags:  []string{"read_only", "bounded_output"},
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false},
	}}, openAIAdapterCapabilities{})
	if len(tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(tools))
	}
	if !strings.Contains(tools[0].Description, "Reads a file.") {
		t.Fatalf("tool description = %q, want base description", tools[0].Description)
	}
	if !strings.Contains(tools[0].Description, "Output notes: Returns bounded slices with truncation markers when needed.") {
		t.Fatalf("tool description = %q, want output notes", tools[0].Description)
	}
	if !strings.Contains(tools[0].Description, "Safety tags: read_only, bounded_output.") {
		t.Fatalf("tool description = %q, want safety tags", tools[0].Description)
	}
}

func TestBuildResponsesToolsUsesNativeWebSearchWhenSupported(t *testing.T) {
	t.Parallel()

	webSearch := agent.ToolDefinition{
		Name:        agent.ToolNameWebSearch,
		Description: "Search the web for current information.",
		Kind:        agent.ToolKindFunction,
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []string{"query"}, "additionalProperties": false},
		Interop: agent.ToolInterop{
			OpenAIPreferredKind: agent.ToolBoundaryKindWebSearch,
			OpenAIFallbackKind:  agent.ToolBoundaryKindFunction,
		},
	}
	tools := buildResponsesTools([]agent.ToolDefinition{webSearch}, openAIAdapterCapabilities{SupportsHostedWebSearch: true})
	if len(tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(tools))
	}
	if tools[0].Type != string(agent.ToolBoundaryKindWebSearch) {
		t.Fatalf("tools[0].Type = %q, want native web_search", tools[0].Type)
	}
	if tools[0].Name != "" {
		t.Fatalf("tools[0].Name = %q, want empty built-in tool name", tools[0].Name)
	}
	if tools[0].Description != "" {
		t.Fatalf("tools[0].Description = %q, want empty built-in tool description", tools[0].Description)
	}
	instructions := buildResponsesInstructions([]agent.Message{{
		Role:    agent.MessageRoleSystem,
		Content: "test prompt",
	}}, []agent.ToolDefinition{webSearch}, openAIAdapterCapabilities{SupportsHostedWebSearch: true})
	if !strings.Contains(instructions, "Provider-native built-in tools may run internally before the final response.") {
		t.Fatalf("instructions = %q, want built-in tool guidance", instructions)
	}
}

func TestParseResponsesDecisionIgnoresWebSearchCallsAndReturnsFinalAnswer(t *testing.T) {
	t.Parallel()

	decision, err := parseResponsesDecision(responsesResponse{
		OutputText: strictFinalText("searched", "done"),
		Output: []responsesOutputItem{
			{Type: "web_search_call"},
			{
				Type: "message",
				Role: agent.MessageRoleAssistant,
				Content: []responsesOutputContent{{
					Type: "output_text",
					Text: strictFinalText("searched", "done"),
				}},
			},
		},
	}, nil, openAIAdapterCapabilities{SupportsHostedWebSearch: true})
	if err != nil {
		t.Fatalf("parseResponsesDecision() error = %v", err)
	}
	if decision.Finish == nil {
		t.Fatalf("decision.Finish = %#v, want final response", decision.Finish)
	}
	if decision.Finish.Value != "done" {
		t.Fatalf("decision.Finish.Value = %#v, want %q", decision.Finish.Value, "done")
	}
}

func TestDriverNextForwardsPromptCacheSettings(t *testing.T) {
	t.Parallel()

	server, requests := newResponsesTestServer(t, []responsesTestServerResponse{
		{OutputText: strictFinalText("inspect", "done")},
	})
	defer server.Close()

	driver, err := New(Config{
		APIKey:               "test-key",
		BaseURL:              server.URL,
		Model:                "gpt-5.4",
		PromptCacheKey:       "agentrepl:gpt-test",
		PromptCacheRetention: "24h",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = driver.Next(context.Background(), agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleUser, Content: "list files"},
		},
	})
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}

	snapshot := requests.snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("len(requests) = %d, want 1", len(snapshot))
	}
	if snapshot[0].PromptCacheKey != "agentrepl:gpt-test" {
		t.Fatalf("request prompt cache key = %q, want %q", snapshot[0].PromptCacheKey, "agentrepl:gpt-test")
	}
	if snapshot[0].PromptCacheRetention != "24h" {
		t.Fatalf("request prompt cache retention = %q, want %q", snapshot[0].PromptCacheRetention, "24h")
	}
}

func TestDriverNextForwardsResponsesPromptCacheSettings(t *testing.T) {
	t.Parallel()

	server, requests := newResponsesTestServer(t, []responsesTestServerResponse{
		{OutputText: strictFinalText("done", "done")},
	})
	defer server.Close()

	driver, err := New(Config{
		APIKey:               "test-key",
		BaseURL:              server.URL,
		Model:                "gpt-5.4",
		PromptCacheKey:       "agentrepl:gpt-5.4",
		PromptCacheRetention: "in_memory",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = driver.Next(context.Background(), agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleUser, Content: "list files"},
		},
		Tools: []agent.ToolDefinition{{
			Name:        agent.ToolNameReadFile,
			Description: "Reads a file.",
			Kind:        agent.ToolKindFunction,
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false},
		}},
	})
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}

	snapshot := requests.snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("len(requests) = %d, want 1", len(snapshot))
	}
	if snapshot[0].PromptCacheKey != "agentrepl:gpt-5.4" {
		t.Fatalf("request prompt cache key = %q, want %q", snapshot[0].PromptCacheKey, "agentrepl:gpt-5.4")
	}
	if snapshot[0].PromptCacheRetention != "in-memory" {
		t.Fatalf("request prompt cache retention = %q, want %q", snapshot[0].PromptCacheRetention, "in-memory")
	}
}

func TestDriverDescribeImageUsesResponsesImageInput(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(responsesResponse{
			OutputText: "A tiny placeholder image.",
		})
	}))
	defer server.Close()

	driver := newTestDriver(t, server.URL)
	response, err := driver.DescribeImage(context.Background(), agent.ImageDescriptionRequest{
		Prompt:    "Describe the image.",
		MediaType: "image/png",
		Data:      []byte("image-bytes"),
	})
	if err != nil {
		t.Fatalf("DescribeImage() error = %v", err)
	}
	if response.Provider != "openai" {
		t.Fatalf("response.Provider = %q, want %q", response.Provider, "openai")
	}
	if response.Description != "A tiny placeholder image." {
		t.Fatalf("response.Description = %q, want response text", response.Description)
	}
	if captured["model"] != "gpt-5.4" {
		t.Fatalf("request model = %#v, want %q", captured["model"], "gpt-5.4")
	}
	input, ok := captured["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("request input = %#v, want single message", captured["input"])
	}
	message, ok := input[0].(map[string]any)
	if !ok {
		t.Fatalf("request input[0] = %#v, want object", input[0])
	}
	if message["role"] != agent.MessageRoleUser {
		t.Fatalf("request role = %#v, want %q", message["role"], agent.MessageRoleUser)
	}
	content, ok := message["content"].([]any)
	if !ok || len(content) != 2 {
		t.Fatalf("request content = %#v, want text and image parts", message["content"])
	}
	textPart, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("text part = %#v, want object", content[0])
	}
	if textPart["type"] != "input_text" || textPart["text"] != "Describe the image." {
		t.Fatalf("text part = %#v, want input_text prompt", textPart)
	}
	imagePart, ok := content[1].(map[string]any)
	if !ok {
		t.Fatalf("image part = %#v, want object", content[1])
	}
	if imagePart["type"] != "input_image" {
		t.Fatalf("image part type = %#v, want input_image", imagePart["type"])
	}
	if imagePart["detail"] != "auto" {
		t.Fatalf("image part detail = %#v, want auto", imagePart["detail"])
	}
	if imagePart["image_url"] != "data:image/png;base64,aW1hZ2UtYnl0ZXM=" {
		t.Fatalf("image part image_url = %#v, want data URL", imagePart["image_url"])
	}
}

func TestDriverDescribeImageUsesResponsesImageURLInput(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(responsesResponse{
			OutputText: "A remote placeholder image.",
		})
	}))
	defer server.Close()

	driver := newTestDriver(t, server.URL)
	response, err := driver.DescribeImage(context.Background(), agent.ImageDescriptionRequest{
		Prompt:   "Describe the remote image.",
		ImageURL: "https://example.com/images/pixel.png",
	})
	if err != nil {
		t.Fatalf("DescribeImage() error = %v", err)
	}
	if response.Provider != "openai" {
		t.Fatalf("response.Provider = %q, want %q", response.Provider, "openai")
	}
	if response.Description != "A remote placeholder image." {
		t.Fatalf("response.Description = %q, want response text", response.Description)
	}
	input, ok := captured["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("request input = %#v, want single message", captured["input"])
	}
	message, ok := input[0].(map[string]any)
	if !ok {
		t.Fatalf("request input[0] = %#v, want object", input[0])
	}
	content, ok := message["content"].([]any)
	if !ok || len(content) != 2 {
		t.Fatalf("request content = %#v, want text and image parts", message["content"])
	}
	imagePart, ok := content[1].(map[string]any)
	if !ok {
		t.Fatalf("image part = %#v, want object", content[1])
	}
	if imagePart["type"] != "input_image" {
		t.Fatalf("image part type = %#v, want input_image", imagePart["type"])
	}
	if imagePart["image_url"] != "https://example.com/images/pixel.png" {
		t.Fatalf("image part image_url = %#v, want public URL", imagePart["image_url"])
	}
	if imagePart["detail"] != "auto" {
		t.Fatalf("image part detail = %#v, want auto", imagePart["detail"])
	}
}

type responsesRequestLog struct {
	mu       sync.Mutex
	requests []responsesRequest
}

type responsesTestServerResponse struct {
	ID           string
	Output       []responsesOutputItem
	OutputText   string
	CachedTokens int
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func (l *responsesRequestLog) append(req responsesRequest) {
	l.mu.Lock()
	l.requests = append(l.requests, req)
	l.mu.Unlock()
}

func (l *responsesRequestLog) snapshot() []responsesRequest {
	l.mu.Lock()
	defer l.mu.Unlock()

	out := make([]responsesRequest, len(l.requests))
	copy(out, l.requests)
	return out
}

func newResponsesTestServer(t *testing.T, responses []responsesTestServerResponse) (*httptest.Server, *responsesRequestLog) {
	t.Helper()

	var (
		mu    sync.Mutex
		index int
	)
	log := &responsesRequestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}

		var req responsesRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		log.append(req)

		mu.Lock()
		defer mu.Unlock()
		if index >= len(responses) {
			t.Fatalf("received more requests than responses")
		}
		response := responses[index]
		index++
		_ = json.NewEncoder(w).Encode(responsesResponse{
			ID:         response.ID,
			Output:     response.Output,
			OutputText: response.OutputText,
			Usage: responsesUsage{
				InputTokensDetails: responsesInputTokensDetails{
					CachedTokens: response.CachedTokens,
				},
			},
		})
	}))
	return server, log
}

func TestDriverNextSendsToolsAndParsesToolCalls(t *testing.T) {
	t.Parallel()

	server, requests := newResponsesTestServer(t, []responsesTestServerResponse{
		{
			Output: []responsesOutputItem{{
				Type:      "function_call",
				CallID:    "call_123",
				Name:      agent.ToolNameReadFile,
				Arguments: `{"file_path":"/tmp/demo.txt"}`,
			}},
		},
	})
	defer server.Close()

	driver := newTestDriver(t, server.URL)
	decision, err := driver.Next(context.Background(), agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleSystem, Content: "test prompt"},
			{Role: agent.MessageRoleUser, Content: "read the file"},
		},
		Tools: []agent.ToolDefinition{
			{
				Name:              agent.ToolNameReadFile,
				Description:       "Reads a local file with bounded output.",
				Kind:              agent.ToolKindFunction,
				Parameters:        map[string]any{"type": "object", "properties": map[string]any{"file_path": map[string]any{"type": "string"}}, "required": []string{"file_path"}, "additionalProperties": false},
				RequiredArguments: []string{"file_path"},
			},
			{
				Name:         agent.ToolNameApplyPatch,
				Description:  "Apply a structured patch to local files.",
				Kind:         agent.ToolKindCustom,
				CustomFormat: &agent.ToolFormat{Type: "grammar", Syntax: "lark", Definition: "start: PATCH\nPATCH: /.+/s"},
				Interop: agent.ToolInterop{
					OpenAIPreferredKind: agent.ToolBoundaryKindCustom,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if decision.Tool == nil {
		t.Fatal("decision.Tool = nil, want tool decision")
	}
	if decision.Tool.Name != agent.ToolNameReadFile {
		t.Fatalf("decision.Tool.Name = %q, want %q", decision.Tool.Name, agent.ToolNameReadFile)
	}
	if decision.Tool.Kind != agent.ToolKindFunction {
		t.Fatalf("decision.Tool.Kind = %q, want %q", decision.Tool.Kind, agent.ToolKindFunction)
	}
	if decision.Tool.Input != `{"file_path":"/tmp/demo.txt"}` {
		t.Fatalf("decision.Tool.Input = %q, want read_file arguments", decision.Tool.Input)
	}

	snapshot := requests.snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("len(requests) = %d, want 1", len(snapshot))
	}
	if snapshot[0].ToolChoice != "auto" {
		t.Fatalf("request tool_choice = %q, want auto", snapshot[0].ToolChoice)
	}
	if snapshot[0].ParallelToolCalls == nil {
		t.Fatal("request parallel_tool_calls = nil, want explicit false")
	}
	if *snapshot[0].ParallelToolCalls {
		t.Fatal("request parallel_tool_calls = true, want false")
	}
	if snapshot[0].Reasoning == nil {
		t.Fatal("request reasoning = nil, want summary config")
	}
	if snapshot[0].Reasoning.Summary != "auto" {
		t.Fatalf("request reasoning summary = %q, want %q", snapshot[0].Reasoning.Summary, "auto")
	}
	if len(snapshot[0].Tools) != 2 {
		t.Fatalf("len(request tools) = %d, want 2", len(snapshot[0].Tools))
	}
	if snapshot[0].Tools[0].Type != "function" || snapshot[0].Tools[0].Name != agent.ToolNameReadFile {
		t.Fatalf("first tool = %#v, want function read_file", snapshot[0].Tools[0])
	}
	if snapshot[0].Tools[1].Type != "custom" || snapshot[0].Tools[1].Name != agent.ToolNameApplyPatch {
		t.Fatalf("second tool = %#v, want custom apply_patch", snapshot[0].Tools[1])
	}
}

func TestParseResponsesDecisionPreservesAssistantTextForToolCalls(t *testing.T) {
	t.Parallel()

	decision, err := parseResponsesDecision(responsesResponse{
		Output: []responsesOutputItem{
			{
				Type: "message",
				Content: []responsesOutputContent{{
					Type: "output_text",
					Text: "inspect the config before reading it",
				}},
			},
			{
				Type:      "function_call",
				CallID:    "call_1",
				Name:      agent.ToolNameReadFile,
				Arguments: `{"file_path":"/tmp/demo.txt"}`,
			},
		},
	}, []agent.ToolDefinition{{
		Name: agent.ToolNameReadFile,
		Kind: agent.ToolKindFunction,
	}}, openAIAdapterCapabilities{})
	if err != nil {
		t.Fatalf("parseResponsesDecision() error = %v", err)
	}
	if decision.Thought != "inspect the config before reading it" {
		t.Fatalf("decision.Thought = %q, want assistant text thought", decision.Thought)
	}
	if decision.Tool == nil || decision.Tool.Name != agent.ToolNameReadFile {
		t.Fatalf("decision.Tool = %#v, want read_file", decision.Tool)
	}
}

func TestParseResponsesDecisionPreservesReasoningSummaryForToolCalls(t *testing.T) {
	t.Parallel()

	decision, err := parseResponsesDecision(responsesResponse{
		Output: []responsesOutputItem{
			{
				Type: "reasoning",
				Summary: []responsesOutputContent{{
					Type: "summary_text",
					Text: "inspect the config before reading it",
				}},
			},
			{
				Type:      "function_call",
				CallID:    "call_1",
				Name:      agent.ToolNameReadFile,
				Arguments: `{"file_path":"/tmp/demo.txt"}`,
			},
		},
	}, []agent.ToolDefinition{{
		Name: agent.ToolNameReadFile,
		Kind: agent.ToolKindFunction,
	}}, openAIAdapterCapabilities{})
	if err != nil {
		t.Fatalf("parseResponsesDecision() error = %v", err)
	}
	if decision.Thought != "inspect the config before reading it" {
		t.Fatalf("decision.Thought = %q, want reasoning summary", decision.Thought)
	}
	if decision.Tool == nil || decision.Tool.Name != agent.ToolNameReadFile {
		t.Fatalf("decision.Tool = %#v, want read_file", decision.Tool)
	}
}

func TestParseResponsesDecisionMergesReasoningSummaryAndAssistantTextForToolCalls(t *testing.T) {
	t.Parallel()

	decision, err := parseResponsesDecision(responsesResponse{
		Output: []responsesOutputItem{
			{
				Type: "reasoning",
				Summary: []responsesOutputContent{{
					Type: "summary_text",
					Text: "inspect the config before reading it",
				}},
			},
			{
				Type: "message",
				Content: []responsesOutputContent{{
					Type: "output_text",
					Text: "read the config file next",
				}},
			},
			{
				Type:      "function_call",
				CallID:    "call_1",
				Name:      agent.ToolNameReadFile,
				Arguments: `{"file_path":"/tmp/demo.txt"}`,
			},
		},
	}, []agent.ToolDefinition{{
		Name: agent.ToolNameReadFile,
		Kind: agent.ToolKindFunction,
	}}, openAIAdapterCapabilities{})
	if err != nil {
		t.Fatalf("parseResponsesDecision() error = %v", err)
	}
	if got := decision.Thought; got != "inspect the config before reading it\nread the config file next" {
		t.Fatalf("decision.Thought = %q, want merged reasoning summary and assistant text", got)
	}
	if decision.Tool == nil || decision.Tool.Name != agent.ToolNameReadFile {
		t.Fatalf("decision.Tool = %#v, want read_file", decision.Tool)
	}
}

func TestParseResponsesDecisionRejectsRefusalOutsideStrictSchema(t *testing.T) {
	t.Parallel()

	_, err := parseResponsesDecision(responsesResponse{
		Output: []responsesOutputItem{{
			Type: "message",
			Content: []responsesOutputContent{{
				Type:    "refusal",
				Refusal: "I can't help with that request.",
			}},
		}},
	}, nil, openAIAdapterCapabilities{})
	if err != nil {
		if !strings.Contains(err.Error(), "refused request") {
			t.Fatalf("parseResponsesDecision() error = %v, want refusal rejection", err)
		}
		return
	}
	t.Fatal("parseResponsesDecision() error = nil, want refusal rejection")
}

func TestDriverNextSendsResponsesReasoningObjectForToolRequests(t *testing.T) {
	t.Parallel()

	server, requests := newResponsesTestServer(t, []responsesTestServerResponse{
		{OutputText: strictFinalText("done", "done")},
	})
	defer server.Close()

	driver, err := New(Config{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Model:   "gpt-5.4-xhigh",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = driver.Next(context.Background(), agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleUser, Content: "say done"},
		},
		Tools: []agent.ToolDefinition{{
			Name:        agent.ToolNameReadFile,
			Description: "Reads a file.",
			Kind:        agent.ToolKindFunction,
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false},
		}},
	})
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}

	snapshot := requests.snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("len(requests) = %d, want 1", len(snapshot))
	}
	if snapshot[0].Model != "gpt-5.4" {
		t.Fatalf("request model = %q, want %q", snapshot[0].Model, "gpt-5.4")
	}
	if snapshot[0].Reasoning == nil {
		t.Fatal("request reasoning = nil, want effort object")
	}
	if snapshot[0].Reasoning.Effort != "high" {
		t.Fatalf("request reasoning effort = %q, want %q", snapshot[0].Reasoning.Effort, "high")
	}
	if snapshot[0].Reasoning.Summary != "auto" {
		t.Fatalf("request reasoning summary = %q, want %q", snapshot[0].Reasoning.Summary, "auto")
	}
}

func TestDriverNextCapturesCachedTokensFromResponsesUsage(t *testing.T) {
	t.Parallel()

	server, _ := newResponsesTestServer(t, []responsesTestServerResponse{
		{
			OutputText:   strictFinalText("done", "done"),
			CachedTokens: 1536,
		},
	})
	defer server.Close()

	driver := newTestDriver(t, server.URL)
	decision, err := driver.Next(context.Background(), agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleUser, Content: "say done"},
		},
		Tools: []agent.ToolDefinition{{
			Name:        agent.ToolNameReadFile,
			Description: "Reads a file.",
			Kind:        agent.ToolKindFunction,
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false},
		}},
	})
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if decision.Usage.CachedTokens != 1536 {
		t.Fatalf("decision.Usage.CachedTokens = %d, want %d", decision.Usage.CachedTokens, 1536)
	}
}

func TestRequestMarshalingIncludesExplicitFalseParallelToolCalls(t *testing.T) {
	t.Parallel()

	responsesBody, err := json.Marshal(responsesRequest{
		Model:             "gpt-test",
		Input:             []responsesInputItem{{Role: agent.MessageRoleUser, Content: "hi"}},
		ToolChoice:        "auto",
		ParallelToolCalls: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("marshal responsesRequest: %v", err)
	}
	if !strings.Contains(string(responsesBody), `"parallel_tool_calls":false`) {
		t.Fatalf("responses request JSON = %s, want explicit parallel_tool_calls false", string(responsesBody))
	}
}

func TestDriverNextTreatsResponsesOutputTextAsFinish(t *testing.T) {
	t.Parallel()

	server, _ := newResponsesTestServer(t, []responsesTestServerResponse{
		{OutputText: strictFinalText("all work is complete", "done")},
	})
	defer server.Close()

	driver := newTestDriver(t, server.URL)
	decision, err := driver.Next(context.Background(), agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleUser, Content: "say done"},
		},
		Tools: []agent.ToolDefinition{{
			Name:        agent.ToolNameReadFile,
			Description: "Reads a file.",
			Kind:        agent.ToolKindFunction,
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false},
		}},
	})
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if decision.Finish == nil {
		t.Fatal("decision.Finish = nil, want finish decision")
	}
	if decision.Thought != "all work is complete" {
		t.Fatalf("decision.Thought = %q, want %q", decision.Thought, "all work is complete")
	}
	if got := decision.Finish.Value; got != "done" {
		t.Fatalf("decision.Finish.Value = %#v, want %q", got, "done")
	}
}

func TestDriverNextParsesStructuredResponsesFinishThought(t *testing.T) {
	t.Parallel()

	server, _ := newResponsesTestServer(t, []responsesTestServerResponse{
		{OutputText: strictFinalText("all work is complete", "done")},
	})
	defer server.Close()

	driver := newTestDriver(t, server.URL)
	decision, err := driver.Next(context.Background(), agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleUser, Content: "say done"},
		},
		Tools: []agent.ToolDefinition{{
			Name:        agent.ToolNameReadFile,
			Description: "Reads a file.",
			Kind:        agent.ToolKindFunction,
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false},
		}},
	})
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if decision.Finish == nil {
		t.Fatal("decision.Finish = nil, want finish decision")
	}
	if got := decision.Thought; got != "all work is complete" {
		t.Fatalf("decision.Thought = %q, want %q", got, "all work is complete")
	}
	if got := decision.Finish.Value; got != "done" {
		t.Fatalf("decision.Finish.Value = %#v, want %q", got, "done")
	}
}

func TestDriverNextMergesReasoningIntoStructuredResponsesFinishThought(t *testing.T) {
	t.Parallel()

	server, _ := newResponsesTestServer(t, []responsesTestServerResponse{
		{
			OutputText: strictFinalText("all work is complete", "done"),
			Output: []responsesOutputItem{{
				Type: "reasoning",
				Summary: []responsesOutputContent{{
					Type: "summary_text",
					Text: "inspect the result before finishing",
				}},
			}},
		},
	})
	defer server.Close()

	driver := newTestDriver(t, server.URL)
	decision, err := driver.Next(context.Background(), agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleUser, Content: "say done"},
		},
		Tools: []agent.ToolDefinition{{
			Name:        agent.ToolNameReadFile,
			Description: "Reads a file.",
			Kind:        agent.ToolKindFunction,
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false},
		}},
	})
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if decision.Finish == nil {
		t.Fatal("decision.Finish = nil, want finish decision")
	}
	if got := decision.Thought; got != "inspect the result before finishing\nall work is complete" {
		t.Fatalf("decision.Thought = %q, want merged final-turn reasoning", got)
	}
	if got := decision.Finish.Value; got != "done" {
		t.Fatalf("decision.Finish.Value = %#v, want %q", got, "done")
	}
}

func TestDriverNextAcceptsStructuredResponsesFinishWithoutThought(t *testing.T) {
	t.Parallel()

	server, _ := newResponsesTestServer(t, []responsesTestServerResponse{
		{OutputText: `{"result_json":"\"done\""}`},
	})
	defer server.Close()

	driver := newTestDriver(t, server.URL)
	decision, err := driver.Next(context.Background(), agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleUser, Content: "say done"},
		},
		Tools: []agent.ToolDefinition{{
			Name:        agent.ToolNameReadFile,
			Description: "Reads a file.",
			Kind:        agent.ToolKindFunction,
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false},
		}},
	})
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if decision.Finish == nil {
		t.Fatal("decision.Finish = nil, want finish decision")
	}
	if got := decision.Thought; got != "" {
		t.Fatalf("decision.Thought = %q, want empty thought", got)
	}
	if got := decision.Finish.Value; got != "done" {
		t.Fatalf("decision.Finish.Value = %#v, want %q", got, "done")
	}
}

func TestParseStrictFinalDecisionDecodesStructuredFinishObjectString(t *testing.T) {
	t.Parallel()

	decision, err := parseStrictFinalDecision(strictFinalText("all work is complete", map[string]any{
		"status": "done",
		"count":  2,
	}))
	if err != nil {
		t.Fatalf("parseStrictFinalDecision() error = %v", err)
	}
	if decision.Finish == nil {
		t.Fatal("decision.Finish = nil, want finish decision")
	}
	value, ok := decision.Finish.Value.(map[string]any)
	if !ok {
		t.Fatalf("decision.Finish.Value = %#v, want object", decision.Finish.Value)
	}
	if value["status"] != "done" {
		t.Fatalf("decision.Finish.Value[status] = %#v, want %q", value["status"], "done")
	}
	if value["count"] != float64(2) {
		t.Fatalf("decision.Finish.Value[count] = %#v, want %v", value["count"], 2)
	}
}

func TestDriverNextRejectsPlainAssistantTextOutsideStrictSchema(t *testing.T) {
	t.Parallel()

	server, _ := newResponsesTestServer(t, []responsesTestServerResponse{
		{OutputText: "done"},
	})
	defer server.Close()

	driver := newTestDriver(t, server.URL)
	_, err := driver.Next(context.Background(), agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleUser, Content: "say done"},
		},
	})
	if err == nil {
		t.Fatal("Next() error = nil, want strict final parser rejection")
	}
}

func TestBuildOpenAIToolAdaptersFallsBackToFunctionTools(t *testing.T) {
	t.Parallel()

	allTools, err := agent.ResolveToolDefinitions(nil, []interp.HostMatcher{{Glob: "*"}})
	if err != nil {
		t.Fatalf("ResolveToolDefinitions() error = %v", err)
	}
	allowed := map[string]struct{}{
		agent.ToolNameApplyPatch: {},
		agent.ToolNameRunShell:   {},
		agent.ToolNameWebSearch:  {},
	}
	tools := make([]agent.ToolDefinition, 0, len(allowed))
	for _, tool := range allTools {
		if _, ok := allowed[tool.Name]; ok {
			tools = append(tools, tool)
		}
	}
	adapters := buildOpenAIToolAdapters(tools, openAIAdapterCapabilities{})
	if len(adapters) != 3 {
		t.Fatalf("len(adapters) = %d, want 3", len(adapters))
	}
	for _, adapter := range adapters {
		if adapter.BoundaryKind != agent.ToolBoundaryKindFunction {
			t.Fatalf("adapter %q boundary kind = %q, want function fallback", adapter.InternalName, adapter.BoundaryKind)
		}
		if adapter.ExposedName != adapter.InternalName {
			t.Fatalf("adapter %q exposed name = %q, want same name fallback", adapter.InternalName, adapter.ExposedName)
		}
	}
}

func TestParseResponsesToolCallDecisionAcceptsFunctionCall(t *testing.T) {
	t.Parallel()

	decision, err := parseResponsesToolCallDecision(responsesOutputItem{
		Type:      "function_call",
		Name:      agent.ToolNameReadFile,
		Arguments: `{"file_path":"/tmp/demo.txt"}`,
		CallID:    "call_1",
	}, []agent.ToolDefinition{{
		Name: agent.ToolNameReadFile,
		Kind: agent.ToolKindFunction,
	}}, openAIAdapterCapabilities{})
	if err != nil {
		t.Fatalf("parseResponsesToolCallDecision() error = %v", err)
	}
	if decision.Tool == nil || decision.Tool.Name != agent.ToolNameReadFile {
		t.Fatalf("decision.Tool = %#v, want read_file", decision.Tool)
	}
	if decision.Tool.Input != `{"file_path":"/tmp/demo.txt"}` {
		t.Fatalf("decision.Tool.Input = %q, want read_file arguments", decision.Tool.Input)
	}
}

func TestParseResponsesToolCallDecisionAcceptsCustomToolCall(t *testing.T) {
	t.Parallel()

	decision, err := parseResponsesToolCallDecision(responsesOutputItem{
		Type:   "custom_tool_call",
		Name:   agent.ToolNameApplyPatch,
		Input:  "*** Begin Patch\n*** End Patch",
		CallID: "call_2",
	}, []agent.ToolDefinition{{
		Name: agent.ToolNameApplyPatch,
		Kind: agent.ToolKindCustom,
	}}, openAIAdapterCapabilities{})
	if err != nil {
		t.Fatalf("parseResponsesToolCallDecision() error = %v", err)
	}
	if decision.Tool == nil || decision.Tool.Kind != agent.ToolKindCustom {
		t.Fatalf("decision.Tool = %#v, want custom apply_patch tool", decision.Tool)
	}
}

func TestParseStrictFinalDecisionRejectsLegacyLocalShellCallJSON(t *testing.T) {
	t.Parallel()

	_, err := parseStrictFinalDecision(`{"type":"local_shell_call","call_id":"call_3","action":{"type":"exec","command":["bash","-lc","ls -la"],"working_directory":"/workspace","timeout_ms":30000}}`)
	if err == nil {
		t.Fatal("parseStrictFinalDecision() error = nil, want legacy wrapper rejection")
	}
}

func TestParseResponsesToolCallDecisionRejectsUnavailableCustomToolCall(t *testing.T) {
	t.Parallel()

	_, err := parseResponsesToolCallDecision(responsesOutputItem{
		Type:   "custom_tool_call",
		Name:   agent.ToolNameApplyPatch,
		Input:  "*** Begin Patch\n*** End Patch",
		CallID: "call_2",
	}, nil, openAIAdapterCapabilities{SupportsCustomTools: true})
	if err == nil {
		t.Fatal("parseResponsesToolCallDecision() error = nil, want unavailable tool rejection")
	}
	if !strings.Contains(err.Error(), `unavailable tool "apply_patch"`) {
		t.Fatalf("parseResponsesToolCallDecision() error = %v, want unavailable tool rejection", err)
	}
}

func TestParseStrictFinalDecisionRejectsLegacyToolWrapperJSON(t *testing.T) {
	t.Parallel()

	_, err := parseStrictFinalDecision(`{"type":"tool","name":"apply_patch","args":{"patch":"*** Begin Patch\n*** End Patch"}}`)
	if err == nil {
		t.Fatal("parseStrictFinalDecision() error = nil, want legacy wrapper rejection")
	}
}

func TestParseStrictFinalDecisionRejectsLegacyCustomToolJSON(t *testing.T) {
	t.Parallel()

	_, err := parseStrictFinalDecision(`{"type":"tool","name":"apply_patch","args":{"patch":"*** Begin Patch\n*** End Patch"},"thought":"patch it"}`)
	if err == nil {
		t.Fatal("parseStrictFinalDecision() error = nil, want legacy wrapper rejection")
	}
}

func TestParseResponsesToolCallDecisionRejectsUnavailableCustomTool(t *testing.T) {
	t.Parallel()

	_, err := parseResponsesToolCallDecision(responsesOutputItem{
		Type:   "custom_tool_call",
		Name:   agent.ToolNameApplyPatch,
		Input:  "*** Begin Patch\n*** End Patch",
		CallID: "call_2",
	}, nil, openAIAdapterCapabilities{SupportsCustomTools: true})
	if err == nil {
		t.Fatal("parseResponsesToolCallDecision() error = nil, want unavailable tool rejection")
	}
	if !strings.Contains(err.Error(), `unavailable tool "apply_patch"`) {
		t.Fatalf("parseResponsesToolCallDecision() error = %v, want unavailable tool rejection", err)
	}
}

func newTestDriver(t *testing.T, baseURL string) *Driver {
	t.Helper()

	driver, err := New(Config{
		APIKey:  "test-key",
		BaseURL: baseURL,
		Model:   "gpt-5.4",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return driver
}

func strictFinalText(thought string, value any) string {
	return agent.StrictFinalResponseExample(thought, value)
}
