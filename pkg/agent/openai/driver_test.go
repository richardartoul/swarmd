package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/richardartoul/swarmd/pkg/agent"
)

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
		Model:        "gpt-test",
		SystemPrompt: "test prompt",
	}); err == nil {
		t.Fatal("New() error = nil, want system prompt rejection")
	}
	if _, err := New(Config{
		APIKey:               "test-key",
		Model:                "gpt-test",
		PreserveConversation: true,
	}); err == nil {
		t.Fatal("New() error = nil, want preserve conversation rejection")
	}
}

func TestNewRejectsInvalidPromptCacheRetention(t *testing.T) {
	t.Parallel()

	if _, err := New(Config{
		APIKey:               "test-key",
		Model:                "gpt-test",
		PromptCacheRetention: "forever",
	}); err == nil {
		t.Fatal("New() error = nil, want invalid prompt cache retention rejection")
	}
}

func TestDriverNextParsesShellDecision(t *testing.T) {
	t.Parallel()

	server, requests := newResponsesTestServer(t, []responsesTestServerResponse{
		{OutputText: `{"type":"shell","thought":"inspect","shell":"ls"}`},
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
	if decision.Shell == nil || decision.Shell.Source != "ls" {
		t.Fatalf("decision.Shell = %#v, want ls", decision.Shell)
	}
	if decision.Finish != nil {
		t.Fatalf("decision.Finish = %#v, want nil", decision.Finish)
	}
	if decision.Usage.CachedTokens != 0 {
		t.Fatalf("decision.Usage.CachedTokens = %d, want 0", decision.Usage.CachedTokens)
	}

	if len(requests.snapshot()) != 1 {
		t.Fatalf("len(requests) = %d, want 1", len(requests.snapshot()))
	}
	got := requests.snapshot()[0]
	if got.Model != "gpt-test" {
		t.Fatalf("request model = %q, want %q", got.Model, "gpt-test")
	}
	if got.Reasoning != nil {
		t.Fatalf("request reasoning = %#v, want nil", got.Reasoning)
	}
	if got.ToolChoice != "" {
		t.Fatalf("request tool_choice = %q, want empty without tools", got.ToolChoice)
	}
	if got.ParallelToolCalls != nil {
		t.Fatalf("request parallel_tool_calls = %#v, want nil without tools", got.ParallelToolCalls)
	}
	if len(got.Input) != 2 {
		t.Fatalf("len(request input) = %d, want 2", len(got.Input))
	}
	if got.Input[0].Role != agent.MessageRoleSystem || got.Input[0].Content != "test prompt" {
		t.Fatalf("first request input = %#v, want system prompt", got.Input[0])
	}
	if got.Input[1].Role != agent.MessageRoleUser || got.Input[1].Content != "list the current directory" {
		t.Fatalf("second request input = %#v, want user prompt", got.Input[1])
	}
}

func TestDriverNextSendsReasoningEffortFromModelSuffix(t *testing.T) {
	t.Parallel()

	server, requests := newResponsesTestServer(t, []responsesTestServerResponse{
		{OutputText: `{"type":"shell","thought":"inspect","shell":"ls"}`},
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
}

func TestDriverNextCapturesCachedTokens(t *testing.T) {
	t.Parallel()

	server, _ := newResponsesTestServer(t, []responsesTestServerResponse{
		{
			OutputText:   `{"type":"shell","thought":"inspect","shell":"ls"}`,
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

func TestDriverNextIgnoresTrailingJSONObjects(t *testing.T) {
	t.Parallel()

	server, _ := newResponsesTestServer(t, []responsesTestServerResponse{
		{
			OutputText: "{\"type\":\"shell\",\"thought\":\"inspect\",\"shell\":\"ls -la\"}\n" +
				"{\"type\":\"finish\",\"thought\":\"done\",\"result\":\"ignored\"}",
		},
	})
	defer server.Close()

	driver := newTestDriver(t, server.URL)
	decision, err := driver.Next(context.Background(), agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleUser, Content: "inspect the box"},
		},
	})
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if decision.Shell == nil || decision.Shell.Source != "ls -la" {
		t.Fatalf("decision.Shell = %#v, want ls -la", decision.Shell)
	}
	if decision.Finish != nil {
		t.Fatalf("decision.Finish = %#v, want nil", decision.Finish)
	}
}

func TestDriverNextForwardsPreparedMessages(t *testing.T) {
	t.Parallel()

	server, requests := newResponsesTestServer(t, []responsesTestServerResponse{
		{OutputText: `{"type":"shell","thought":"follow up","shell":"cat note.txt"}`},
	})
	defer server.Close()

	driver := newTestDriver(t, server.URL)
	_, err := driver.Next(context.Background(), agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleSystem, Content: "test prompt"},
			{Role: agent.MessageRoleSystem, Content: "Conversation history across previous triggers:\n..."},
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
	if len(got.Input) != 3 {
		t.Fatalf("len(request input) = %d, want 3", len(got.Input))
	}
	if got.Input[0].Role != agent.MessageRoleSystem || got.Input[0].Content != "test prompt" {
		t.Fatalf("first request input = %#v, want system prompt", got.Input[0])
	}
	if got.Input[1].Role != agent.MessageRoleSystem || got.Input[1].Content != "Conversation history across previous triggers:\n..." {
		t.Fatalf("second request input = %#v, want history", got.Input[1])
	}
	if got.Input[2].Role != agent.MessageRoleUser || got.Input[2].Content != "show it to me" {
		t.Fatalf("third request input = %#v, want user message", got.Input[2])
	}
}

func TestBuildResponsesInputReplaysNativeToolHistory(t *testing.T) {
	t.Parallel()

	input := buildResponsesInput(agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleSystem, Content: "test prompt"},
			{Role: agent.MessageRoleUser, Content: "trigger context"},
			{Role: agent.MessageRoleUser, Content: "Current execution state\nCurrent step number: 2"},
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
	}, openAIAdapterCapabilities{SupportsCustomTools: true})

	if len(input) != 8 {
		t.Fatalf("len(input) = %d, want 8", len(input))
	}
	if input[2].Role != agent.MessageRoleAssistant || input[2].Content != "inspect the file first" {
		t.Fatalf("input[2] = %#v, want assistant thought message", input[2])
	}
	if input[3].Type != "function_call" || input[3].CallID != "step_1" || input[3].Name != agent.ToolNameReadFile {
		t.Fatalf("input[3] = %#v, want function_call replay", input[3])
	}
	if input[3].Arguments != `{"file_path":"/tmp/demo.txt"}` {
		t.Fatalf("input[3].Arguments = %q, want replayed function arguments", input[3].Arguments)
	}
	if input[4].Type != "function_call_output" || input[4].CallID != "step_1" {
		t.Fatalf("input[4] = %#v, want function_call_output replay", input[4])
	}
	if !strings.Contains(input[4].Output, "demo content") {
		t.Fatalf("input[4].Output = %q, want observation output", input[4].Output)
	}
	if input[5].Type != "custom_tool_call" || input[5].CallID != "step_2" || input[5].Name != agent.ToolNameApplyPatch {
		t.Fatalf("input[5] = %#v, want custom_tool_call replay", input[5])
	}
	if input[5].Input != "*** Begin Patch\n*** End Patch" {
		t.Fatalf("input[5].Input = %q, want raw custom tool input", input[5].Input)
	}
	if input[6].Type != "custom_tool_call_output" || input[6].CallID != "step_2" {
		t.Fatalf("input[6] = %#v, want custom_tool_call_output replay", input[6])
	}
	if input[7].Content != "Current execution state\nCurrent step number: 2" {
		t.Fatalf("input[7].Content = %q, want current-state footer last", input[7].Content)
	}
}

func TestDriverNextForwardsPromptCacheSettings(t *testing.T) {
	t.Parallel()

	server, requests := newResponsesTestServer(t, []responsesTestServerResponse{
		{OutputText: `{"type":"shell","thought":"inspect","shell":"ls"}`},
	})
	defer server.Close()

	driver, err := New(Config{
		APIKey:               "test-key",
		BaseURL:              server.URL,
		Model:                "gpt-test",
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
		{OutputText: "done"},
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

type responsesRequestLog struct {
	mu       sync.Mutex
	requests []responsesRequest
}

type responsesTestServerResponse struct {
	Output       []responsesOutputItem
	OutputText   string
	CachedTokens int
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
	if snapshot[0].Reasoning != nil {
		t.Fatalf("request reasoning = %#v, want nil", snapshot[0].Reasoning)
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

func TestDriverNextSendsResponsesReasoningObjectForToolRequests(t *testing.T) {
	t.Parallel()

	server, requests := newResponsesTestServer(t, []responsesTestServerResponse{
		{OutputText: "done"},
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
}

func TestDriverNextCapturesCachedTokensFromResponsesUsage(t *testing.T) {
	t.Parallel()

	server, _ := newResponsesTestServer(t, []responsesTestServerResponse{
		{
			OutputText:   "done",
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
		{OutputText: "done"},
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
	if got := decision.Finish.Value; got != "done" {
		t.Fatalf("decision.Finish.Value = %#v, want %q", got, "done")
	}
}

func TestDriverNextParsesStructuredResponsesFinishThought(t *testing.T) {
	t.Parallel()

	server, _ := newResponsesTestServer(t, []responsesTestServerResponse{
		{OutputText: `{"type":"finish","thought":"all work is complete","result":"done"}`},
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

func TestDriverNextTreatsPlainAssistantTextAsFinish(t *testing.T) {
	t.Parallel()

	server, _ := newResponsesTestServer(t, []responsesTestServerResponse{
		{OutputText: "done"},
	})
	defer server.Close()

	driver := newTestDriver(t, server.URL)
	decision, err := driver.Next(context.Background(), agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleUser, Content: "say done"},
		},
	})
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if decision.Finish == nil {
		t.Fatal("decision.Finish = nil, want finish decision")
	}
	if got := decision.Finish.Value; got != "done" {
		t.Fatalf("decision.Finish.Value = %#v, want %q", got, "done")
	}
}

func TestBuildOpenAIToolAdaptersFallsBackToFunctionTools(t *testing.T) {
	t.Parallel()

	allTools, err := agent.ResolveToolDefinitions(nil, true)
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

func TestParseDecisionAcceptsFunctionCallJSON(t *testing.T) {
	t.Parallel()

	decision, err := parseDecision(`{"type":"function_call","name":"read_file","arguments":"{\"file_path\":\"/tmp/demo.txt\"}","call_id":"call_1"}`, []agent.ToolDefinition{{
		Name: agent.ToolNameReadFile,
		Kind: agent.ToolKindFunction,
	}}, openAIAdapterCapabilities{})
	if err != nil {
		t.Fatalf("parseDecision() error = %v", err)
	}
	if decision.Tool == nil || decision.Tool.Name != agent.ToolNameReadFile {
		t.Fatalf("decision.Tool = %#v, want read_file", decision.Tool)
	}
	if decision.Tool.Input != `{"file_path":"/tmp/demo.txt"}` {
		t.Fatalf("decision.Tool.Input = %q, want read_file arguments", decision.Tool.Input)
	}
}

func TestParseDecisionAcceptsCustomToolCallJSON(t *testing.T) {
	t.Parallel()

	decision, err := parseDecision(`{"type":"custom_tool_call","name":"apply_patch","input":"*** Begin Patch\n*** End Patch","call_id":"call_2"}`, []agent.ToolDefinition{{
		Name: agent.ToolNameApplyPatch,
		Kind: agent.ToolKindCustom,
	}}, openAIAdapterCapabilities{})
	if err != nil {
		t.Fatalf("parseDecision() error = %v", err)
	}
	if decision.Tool == nil || decision.Tool.Kind != agent.ToolKindCustom {
		t.Fatalf("decision.Tool = %#v, want custom apply_patch tool", decision.Tool)
	}
}

func TestParseDecisionAcceptsLocalShellCallJSON(t *testing.T) {
	t.Parallel()

	decision, err := parseDecision(`{"type":"local_shell_call","call_id":"call_3","action":{"type":"exec","command":["bash","-lc","ls -la"],"working_directory":"/workspace","timeout_ms":30000}}`, []agent.ToolDefinition{{
		Name: agent.ToolNameRunShell,
		Kind: agent.ToolKindFunction,
	}}, openAIAdapterCapabilities{})
	if err != nil {
		t.Fatalf("parseDecision() error = %v", err)
	}
	if decision.Tool == nil || decision.Tool.Name != agent.ToolNameRunShell {
		t.Fatalf("decision.Tool = %#v, want run_shell", decision.Tool)
	}
	if !strings.Contains(decision.Tool.Input, `"command":"ls -la"`) {
		t.Fatalf("decision.Tool.Input = %q, want shell command payload", decision.Tool.Input)
	}
	if !strings.Contains(decision.Tool.Input, `"workdir":"/workspace"`) {
		t.Fatalf("decision.Tool.Input = %q, want working directory payload", decision.Tool.Input)
	}
}

func TestParseDecisionRejectsUnavailableCustomToolCallJSON(t *testing.T) {
	t.Parallel()

	_, err := parseDecision(`{"type":"custom_tool_call","name":"apply_patch","input":"*** Begin Patch\n*** End Patch","call_id":"call_2"}`, nil, openAIAdapterCapabilities{SupportsCustomTools: true})
	if err == nil {
		t.Fatal("parseDecision() error = nil, want unavailable tool rejection")
	}
	if !strings.Contains(err.Error(), `unavailable tool "apply_patch"`) {
		t.Fatalf("parseDecision() error = %v, want unavailable tool rejection", err)
	}
}

func TestParseDecisionRejectsUnavailableLegacyToolJSON(t *testing.T) {
	t.Parallel()

	_, err := parseDecision(`{"type":"tool","name":"apply_patch","args":{"patch":"*** Begin Patch\n*** End Patch"}}`, nil, openAIAdapterCapabilities{})
	if err == nil {
		t.Fatal("parseDecision() error = nil, want unavailable tool rejection")
	}
	if !strings.Contains(err.Error(), `unavailable tool "apply_patch"`) {
		t.Fatalf("parseDecision() error = %v, want unavailable tool rejection", err)
	}
}

func TestParseDecisionAcceptsLegacyCustomToolJSON(t *testing.T) {
	t.Parallel()

	decision, err := parseDecision(`{"type":"tool","name":"apply_patch","args":{"patch":"*** Begin Patch\n*** End Patch"},"thought":"patch it"}`, []agent.ToolDefinition{{
		Name: agent.ToolNameApplyPatch,
		Kind: agent.ToolKindCustom,
	}}, openAIAdapterCapabilities{})
	if err != nil {
		t.Fatalf("parseDecision() error = %v", err)
	}
	if decision.Thought != "patch it" {
		t.Fatalf("decision.Thought = %q, want %q", decision.Thought, "patch it")
	}
	if decision.Tool == nil || decision.Tool.Kind != agent.ToolKindCustom {
		t.Fatalf("decision.Tool = %#v, want custom apply_patch tool", decision.Tool)
	}
	if decision.Tool.Input != "*** Begin Patch\n*** End Patch" {
		t.Fatalf("decision.Tool.Input = %q, want unwrapped patch input", decision.Tool.Input)
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
		Model:   "gpt-test",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return driver
}
