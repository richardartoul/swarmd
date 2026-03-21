package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/richardartoul/swarmd/pkg/agent"
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

	if _, err := New(Config{Model: "claude-sonnet-4-6"}); err == nil {
		t.Fatal("New() error = nil, want missing api key error")
	}
	if _, err := New(Config{APIKey: "test-key"}); err == nil {
		t.Fatal("New() error = nil, want missing model error")
	}
}

func TestNewParsesReasoningLevelFromModelSuffix(t *testing.T) {
	t.Parallel()

	driver, err := New(Config{
		APIKey: "test-key",
		Model:  "claude-sonnet-4-6-high",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if driver.model != "claude-sonnet-4-6" {
		t.Fatalf("driver.model = %q, want %q", driver.model, "claude-sonnet-4-6")
	}
	if driver.reasoning != "high" {
		t.Fatalf("driver.reasoning = %q, want %q", driver.reasoning, "high")
	}
}

func TestNewRejectsAgentOwnedPromptSettings(t *testing.T) {
	t.Parallel()

	if _, err := New(Config{
		APIKey:       "test-key",
		Model:        "claude-sonnet-4-6",
		SystemPrompt: "test prompt",
	}); err == nil {
		t.Fatal("New() error = nil, want system prompt rejection")
	}
}

func TestNewRejectsInvalidPromptCacheTTL(t *testing.T) {
	t.Parallel()

	if _, err := New(Config{
		APIKey:         "test-key",
		Model:          "claude-sonnet-4-6",
		PromptCacheTTL: "24h",
	}); err == nil {
		t.Fatal("New() error = nil, want invalid prompt cache ttl rejection")
	}
}

func TestNewRejectsUnsupportedModelForStructuredOutputs(t *testing.T) {
	t.Parallel()

	if _, err := New(Config{
		APIKey: "test-key",
		Model:  "claude-3-5-sonnet-latest",
	}); err == nil {
		t.Fatal("New() error = nil, want unsupported model rejection")
	}
}

func TestDriverNextMovesSystemMessagesIntoTopLevelSystem(t *testing.T) {
	t.Parallel()

	server, requests := newTestServer(t, []testServerResponse{
		{
			Content: []anthropicContentBlock{{
				Type: "text",
				Text: strictFinalText("done", "done"),
			}},
			StopReason: "end_turn",
		},
	})
	defer server.Close()

	driver := newTestDriver(t, server.URL)
	_, err := driver.Next(context.Background(), agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleSystem, Content: "test prompt"},
			{Role: agent.MessageRoleSystem, Content: "tool availability"},
			{Role: agent.MessageRoleUser, Content: "say done"},
			{Role: agent.MessageRoleAssistant, Content: "previous step"},
		},
	})
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}

	snapshot := requests.snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("len(requests) = %d, want 1", len(snapshot))
	}
	if len(snapshot[0].Request.System) != 2 {
		t.Fatalf("len(request system) = %d, want 2", len(snapshot[0].Request.System))
	}
	if snapshot[0].Request.System[0].Type != "text" || snapshot[0].Request.System[0].Text != "test prompt" {
		t.Fatalf("request system[0] = %#v, want base prompt block", snapshot[0].Request.System[0])
	}
	if snapshot[0].Request.System[0].CacheControl != nil {
		t.Fatalf("request system[0].cache_control = %#v, want nil", snapshot[0].Request.System[0].CacheControl)
	}
	if snapshot[0].Request.System[1].Type != "text" || snapshot[0].Request.System[1].Text != "tool availability" {
		t.Fatalf("request system[1] = %#v, want tool availability block", snapshot[0].Request.System[1])
	}
	if snapshot[0].Request.System[1].CacheControl == nil {
		t.Fatal("request system[1].cache_control = nil, want cache breakpoint")
	}
	if snapshot[0].Request.System[1].CacheControl.TTL != "5m" {
		t.Fatalf("request system[1].cache_control.ttl = %q, want %q", snapshot[0].Request.System[1].CacheControl.TTL, "5m")
	}
	if snapshot[0].Request.MaxTokens != DefaultMaxTokens {
		t.Fatalf("request max_tokens = %d, want %d", snapshot[0].Request.MaxTokens, DefaultMaxTokens)
	}
	if len(snapshot[0].Request.Messages) != 2 {
		t.Fatalf("len(request messages) = %d, want 2", len(snapshot[0].Request.Messages))
	}
	if snapshot[0].Request.Messages[0].Role != agent.MessageRoleUser || mustAnthropicStringContent(t, snapshot[0].Request.Messages[0].Content) != "say done" {
		t.Fatalf("first request message = %#v, want user message", snapshot[0].Request.Messages[0])
	}
	if snapshot[0].Request.Messages[1].Role != agent.MessageRoleAssistant || mustAnthropicStringContent(t, snapshot[0].Request.Messages[1].Content) != "previous step" {
		t.Fatalf("second request message = %#v, want assistant message", snapshot[0].Request.Messages[1])
	}
	if got := snapshot[0].Headers.Get("x-api-key"); got != "test-key" {
		t.Fatalf("x-api-key header = %q, want %q", got, "test-key")
	}
	if got := snapshot[0].Headers.Get("anthropic-version"); got != anthropicVersion {
		t.Fatalf("anthropic-version header = %q, want %q", got, anthropicVersion)
	}
}

func TestDriverNextSendsAdaptiveThinkingFromModelSuffix(t *testing.T) {
	t.Parallel()

	server, requests := newTestServer(t, []testServerResponse{
		{
			Content: []anthropicContentBlock{{
				Type: "text",
				Text: strictFinalText("done", "done"),
			}},
			StopReason: "end_turn",
		},
	})
	defer server.Close()

	driver, err := New(Config{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Model:   "claude-sonnet-4-6-medium",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = driver.Next(context.Background(), agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleUser, Content: "say done"},
		},
	})
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}

	snapshot := requests.snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("len(requests) = %d, want 1", len(snapshot))
	}
	if snapshot[0].Request.Model != "claude-sonnet-4-6" {
		t.Fatalf("request model = %q, want %q", snapshot[0].Request.Model, "claude-sonnet-4-6")
	}
	if snapshot[0].Request.Thinking == nil || snapshot[0].Request.Thinking.Type != "adaptive" {
		t.Fatalf("request thinking = %#v, want adaptive thinking", snapshot[0].Request.Thinking)
	}
	if snapshot[0].Request.OutputConfig == nil || snapshot[0].Request.OutputConfig.Effort != "medium" {
		t.Fatalf("request output_config = %#v, want medium effort", snapshot[0].Request.OutputConfig)
	}
	if snapshot[0].Request.OutputConfig.Format == nil || snapshot[0].Request.OutputConfig.Format.Type != "json_schema" {
		t.Fatalf("request output_config.format = %#v, want json_schema strict final format", snapshot[0].Request.OutputConfig.Format)
	}
}

func TestDriverNextDefaultsPromptCacheTTLTo5m(t *testing.T) {
	t.Parallel()

	server, requests := newTestServer(t, []testServerResponse{
		{
			Content: []anthropicContentBlock{{
				Type: "text",
				Text: strictFinalText("done", "done"),
			}},
			StopReason: "end_turn",
		},
	})
	defer server.Close()

	driver, err := New(Config{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Model:   "claude-sonnet-4-6",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = driver.Next(context.Background(), agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleUser, Content: "say done"},
		},
	})
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}

	snapshot := requests.snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("len(requests) = %d, want 1", len(snapshot))
	}
	if snapshot[0].Request.CacheControl == nil {
		t.Fatal("request cache_control = nil, want default prompt cache enabled")
	}
	if snapshot[0].Request.CacheControl.Type != "ephemeral" {
		t.Fatalf("request cache_control.type = %q, want %q", snapshot[0].Request.CacheControl.Type, "ephemeral")
	}
	if snapshot[0].Request.CacheControl.TTL != "5m" {
		t.Fatalf("request cache_control.ttl = %q, want %q", snapshot[0].Request.CacheControl.TTL, "5m")
	}
}

func TestDriverNextForwardsPromptCacheTTL(t *testing.T) {
	t.Parallel()

	server, requests := newTestServer(t, []testServerResponse{
		{
			Content: []anthropicContentBlock{{
				Type: "text",
				Text: strictFinalText("done", "done"),
			}},
			StopReason: "end_turn",
		},
	})
	defer server.Close()

	driver, err := New(Config{
		APIKey:         "test-key",
		BaseURL:        server.URL,
		Model:          "claude-sonnet-4-6",
		PromptCacheTTL: "1h",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = driver.Next(context.Background(), agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleUser, Content: "say done"},
		},
	})
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}

	snapshot := requests.snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("len(requests) = %d, want 1", len(snapshot))
	}
	if snapshot[0].Request.CacheControl == nil {
		t.Fatal("request cache_control = nil, want prompt cache enabled")
	}
	if snapshot[0].Request.CacheControl.Type != "ephemeral" {
		t.Fatalf("request cache_control.type = %q, want %q", snapshot[0].Request.CacheControl.Type, "ephemeral")
	}
	if snapshot[0].Request.CacheControl.TTL != "1h" {
		t.Fatalf("request cache_control.ttl = %q, want %q", snapshot[0].Request.CacheControl.TTL, "1h")
	}
}

func TestDriverNextSendsToolsAndParsesToolUse(t *testing.T) {
	t.Parallel()

	server, requests := newTestServer(t, []testServerResponse{
		{
			Content: []anthropicContentBlock{{
				Type:  "tool_use",
				ID:    "toolu_123",
				Name:  agent.ToolNameReadFile,
				Input: json.RawMessage(`{"file_path":"/tmp/demo.txt"}`),
			}},
			StopReason: "tool_use",
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
				Examples:          []string{`{"file_path":"/tmp/example.txt"}`},
			},
			{
				Name:         agent.ToolNameApplyPatch,
				Description:  "Apply a structured patch to local files.",
				Kind:         agent.ToolKindCustom,
				Strict:       true,
				CustomFormat: &agent.ToolFormat{Type: "grammar", Syntax: "lark", Definition: "start: PATCH\nPATCH: /.+/s"},
				Examples:     []string{"*** Begin Patch\n*** Update File: /workspace/app.txt\n@@\n-old line\n+new line\n*** End Patch"},
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
	if snapshot[0].Request.ToolChoice == nil || snapshot[0].Request.ToolChoice.Type != "auto" {
		t.Fatalf("request tool_choice = %#v, want auto", snapshot[0].Request.ToolChoice)
	}
	if !snapshot[0].Request.ToolChoice.DisableParallelToolUse {
		t.Fatal("request tool_choice.disable_parallel_tool_use = false, want true")
	}
	if snapshot[0].Request.OutputConfig == nil || snapshot[0].Request.OutputConfig.Format == nil {
		t.Fatalf("request output_config = %#v, want strict final format", snapshot[0].Request.OutputConfig)
	}
	if snapshot[0].Request.OutputConfig.Format.Type != "json_schema" {
		t.Fatalf("request output_config.format.type = %q, want %q", snapshot[0].Request.OutputConfig.Format.Type, "json_schema")
	}
	if len(snapshot[0].Request.Tools) != 2 {
		t.Fatalf("len(request tools) = %d, want 2", len(snapshot[0].Request.Tools))
	}
	if snapshot[0].Request.Tools[0].Name != agent.ToolNameReadFile {
		t.Fatalf("first tool = %#v, want read_file", snapshot[0].Request.Tools[0])
	}
	if len(snapshot[0].Request.Tools[0].InputExamples) != 1 {
		t.Fatalf("len(read_file input_examples) = %d, want 1 for function tools", len(snapshot[0].Request.Tools[0].InputExamples))
	}
	if got := snapshot[0].Request.Tools[0].InputExamples[0]["file_path"]; got != "/tmp/example.txt" {
		t.Fatalf("read_file input_examples[0][file_path] = %#v, want %q", got, "/tmp/example.txt")
	}
	if snapshot[0].Request.Tools[0].CacheControl != nil {
		t.Fatalf("first tool cache_control = %#v, want nil", snapshot[0].Request.Tools[0].CacheControl)
	}
	patchSchema := snapshot[0].Request.Tools[1].InputSchema
	if snapshot[0].Request.Tools[1].Name != agent.ToolNameApplyPatch {
		t.Fatalf("second tool = %#v, want apply_patch", snapshot[0].Request.Tools[1])
	}
	if !strings.Contains(snapshot[0].Request.Tools[1].Description, "Input format: grammar/lark.") {
		t.Fatalf("patch tool description = %q, want custom format guidance", snapshot[0].Request.Tools[1].Description)
	}
	if strings.Contains(snapshot[0].Request.Tools[1].Description, "Input definition:") {
		t.Fatalf("patch tool description = %q, want custom format definition omitted from always-on description", snapshot[0].Request.Tools[1].Description)
	}
	if !snapshot[0].Request.Tools[1].Strict {
		t.Fatal("patch tool strict = false, want true")
	}
	if len(snapshot[0].Request.Tools[1].InputExamples) != 1 {
		t.Fatalf("len(patch tool input_examples) = %d, want 1", len(snapshot[0].Request.Tools[1].InputExamples))
	}
	if snapshot[0].Request.Tools[1].CacheControl != nil {
		t.Fatalf("patch tool cache_control = %#v, want nil when system blocks own the static-prefix breakpoint", snapshot[0].Request.Tools[1].CacheControl)
	}
	if got := snapshot[0].Request.Tools[1].InputExamples[0]["patch"]; got != "*** Begin Patch\n*** Update File: /workspace/app.txt\n@@\n-old line\n+new line\n*** End Patch" {
		t.Fatalf("patch tool input_examples[0][patch] = %#v, want wrapped patch example", got)
	}
	properties, ok := patchSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("patch schema properties = %#v, want object", patchSchema["properties"])
	}
	if _, ok := properties["patch"]; !ok {
		t.Fatalf("patch schema = %#v, want patch wrapper property", patchSchema)
	}
	patchProperty, ok := properties["patch"].(map[string]any)
	if !ok {
		t.Fatalf("patch property = %#v, want object", properties["patch"])
	}
	if patchProperty["description"] != `Structured patch text. Provide the full patch body including "*** Begin Patch" and "*** End Patch".` {
		t.Fatalf("patch property description = %#v, want explicit patch wrapper guidance", patchProperty["description"])
	}
}

func TestBuildAnthropicToolsCapsInputExamplesByToolKind(t *testing.T) {
	t.Parallel()

	tools := buildAnthropicTools([]agent.ToolDefinition{
		{
			Name:       agent.ToolNameReadFile,
			Kind:       agent.ToolKindFunction,
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"file_path": map[string]any{"type": "string"}}, "required": []string{"file_path"}, "additionalProperties": false},
			Examples:   []string{`{"file_path":"/tmp/demo.txt"}`},
		},
		{
			Name:         agent.ToolNameApplyPatch,
			Kind:         agent.ToolKindCustom,
			CustomFormat: &agent.ToolFormat{Type: "grammar", Syntax: "lark", Definition: "start: PATCH\nPATCH: /.+/s"},
			Examples: []string{
				"*** Begin Patch\n*** End Patch",
				"*** Begin Patch\n*** Update File: /workspace/demo.txt\n@@\n-old\n+new\n*** End Patch",
			},
		},
	}, "", false)
	if len(tools) != 2 {
		t.Fatalf("len(tools) = %d, want 2", len(tools))
	}
	if len(tools[0].InputExamples) != 1 {
		t.Fatalf("len(read_file input_examples) = %d, want 1", len(tools[0].InputExamples))
	}
	if got := tools[0].InputExamples[0]["file_path"]; got != "/tmp/demo.txt" {
		t.Fatalf("read_file input_examples[0][file_path] = %#v, want %q", got, "/tmp/demo.txt")
	}
	if len(tools[1].InputExamples) != 1 {
		t.Fatalf("len(apply_patch input_examples) = %d, want 1", len(tools[1].InputExamples))
	}
}

func TestBuildAnthropicToolsMovesUniqueHintsIntoDescriptions(t *testing.T) {
	t.Parallel()

	tools := buildAnthropicTools([]agent.ToolDefinition{{
		Name:         agent.ToolNameApplyPatch,
		Description:  "Apply a structured patch.",
		Kind:         agent.ToolKindCustom,
		CustomFormat: &agent.ToolFormat{Type: "grammar", Syntax: "lark", Definition: "start: PATCH\nPATCH: /.+/s"},
		OutputNotes:  "Returns a patch application summary.",
		SafetyTags:   []string{"mutating", "filesystem_write"},
	}}, "", false)
	if len(tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(tools))
	}
	if !strings.Contains(tools[0].Description, "Apply a structured patch.") {
		t.Fatalf("tool description = %q, want base description", tools[0].Description)
	}
	if !strings.Contains(tools[0].Description, "Input format: grammar/lark.") {
		t.Fatalf("tool description = %q, want input format label", tools[0].Description)
	}
	if !strings.Contains(tools[0].Description, "Output notes: Returns a patch application summary.") {
		t.Fatalf("tool description = %q, want output notes", tools[0].Description)
	}
	if !strings.Contains(tools[0].Description, "Safety tags: mutating, filesystem_write.") {
		t.Fatalf("tool description = %q, want safety tags", tools[0].Description)
	}
}

func TestBuildMessagesRequestReplaysFunctionToolUseAndResult(t *testing.T) {
	t.Parallel()

	driver := &Driver{model: "claude-sonnet-4-6", maxTokens: DefaultMaxTokens}
	payload, err := driver.buildMessagesRequest(agent.Request{
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
	})
	if err != nil {
		t.Fatalf("buildMessagesRequest() error = %v", err)
	}
	if len(payload.System) != 1 {
		t.Fatalf("len(payload.System) = %d, want 1", len(payload.System))
	}
	if payload.System[0].Type != "text" || payload.System[0].Text != "test prompt" {
		t.Fatalf("payload.System[0] = %#v, want test prompt block", payload.System[0])
	}
	if payload.System[0].CacheControl != nil {
		t.Fatalf("payload.System[0].cache_control = %#v, want nil without prompt caching", payload.System[0].CacheControl)
	}
	if len(payload.Messages) != 4 {
		t.Fatalf("len(payload.Messages) = %d, want 4", len(payload.Messages))
	}
	if payload.Messages[0].Role != agent.MessageRoleUser || mustAnthropicStringContent(t, payload.Messages[0].Content) != "trigger context" {
		t.Fatalf("payload.Messages[0] = %#v, want trigger context", payload.Messages[0])
	}
	assistantBlocks := mustAnthropicBlocks(t, payload.Messages[1].Content)
	if len(assistantBlocks) != 2 {
		t.Fatalf("len(assistantBlocks) = %d, want 2", len(assistantBlocks))
	}
	if assistantBlocks[0]["type"] != "text" || assistantBlocks[0]["text"] != "inspect the file first" {
		t.Fatalf("assistantBlocks[0] = %#v, want text thought block", assistantBlocks[0])
	}
	if assistantBlocks[1]["type"] != "tool_use" || assistantBlocks[1]["id"] != "step_1" || assistantBlocks[1]["name"] != agent.ToolNameReadFile {
		t.Fatalf("assistantBlocks[1] = %#v, want tool_use block", assistantBlocks[1])
	}
	input, ok := assistantBlocks[1]["input"].(map[string]any)
	if !ok {
		t.Fatalf("assistantBlocks[1][input] = %#v, want object", assistantBlocks[1]["input"])
	}
	if input["file_path"] != "/tmp/demo.txt" {
		t.Fatalf("tool_use input = %#v, want file_path", input)
	}
	userBlocks := mustAnthropicBlocks(t, payload.Messages[2].Content)
	if len(userBlocks) != 1 {
		t.Fatalf("len(userBlocks) = %d, want 1", len(userBlocks))
	}
	if userBlocks[0]["type"] != "tool_result" || userBlocks[0]["tool_use_id"] != "step_1" {
		t.Fatalf("userBlocks[0] = %#v, want tool_result block", userBlocks[0])
	}
	if _, ok := userBlocks[0]["is_error"]; ok {
		t.Fatalf("userBlocks[0] = %#v, want no is_error for successful replay", userBlocks[0])
	}
	if _, ok := userBlocks[0]["cache_control"]; ok {
		t.Fatalf("userBlocks[0] = %#v, want no cache_control without prompt caching", userBlocks[0])
	}
	if !strings.Contains(userBlocks[0]["content"].(string), "demo content") {
		t.Fatalf("tool_result content = %#v, want observation output", userBlocks[0]["content"])
	}
	if mustAnthropicStringContent(t, payload.Messages[3].Content) != testCurrentStateMessage(2) {
		t.Fatalf("payload.Messages[3].Content = %#v, want current-state footer", payload.Messages[3].Content)
	}
}

func TestBuildMessagesRequestInterleavesPriorSessionTurnBeforeCurrentTurn(t *testing.T) {
	t.Parallel()

	driver := &Driver{model: "claude-sonnet-4-6", maxTokens: DefaultMaxTokens}
	payload, err := driver.buildMessagesRequest(agent.Request{
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
			ReadOnly:   true,
		}},
	})
	if err != nil {
		t.Fatalf("buildMessagesRequest() error = %v", err)
	}
	if len(payload.Messages) != 7 {
		t.Fatalf("len(payload.Messages) = %d, want 7", len(payload.Messages))
	}
	if got := mustAnthropicTextContent(t, payload.Messages[0].Content); got != "first question" {
		t.Fatalf("payload.Messages[0] = %q, want %q", got, "first question")
	}
	assistantReplayBlocks := mustAnthropicBlocks(t, payload.Messages[1].Content)
	if assistantReplayBlocks[len(assistantReplayBlocks)-1]["type"] != "tool_use" {
		t.Fatalf("payload.Messages[1] = %#v, want assistant tool replay before current turn", assistantReplayBlocks)
	}
	userReplayBlocks := mustAnthropicBlocks(t, payload.Messages[2].Content)
	if userReplayBlocks[0]["type"] != "tool_result" {
		t.Fatalf("payload.Messages[2] = %#v, want tool result before prior assistant reply", userReplayBlocks)
	}
	if got := mustAnthropicTextContent(t, payload.Messages[3].Content); got != "first answer" {
		t.Fatalf("payload.Messages[3] = %q, want %q", got, "first answer")
	}
	if got := mustAnthropicTextContent(t, payload.Messages[4].Content); got != "second question" {
		t.Fatalf("payload.Messages[4] = %q, want %q", got, "second question")
	}
	if got := mustAnthropicTextContent(t, payload.Messages[5].Content); got != "stable protocol" {
		t.Fatalf("payload.Messages[5] = %q, want %q", got, "stable protocol")
	}
	if got := mustAnthropicTextContent(t, payload.Messages[6].Content); got != testCurrentStateMessage(1) {
		t.Fatalf("payload.Messages[6] = %q, want current-state footer", got)
	}
}

func TestBuildMessagesRequestCachesPriorTurnBoundaryForSessionTurn(t *testing.T) {
	t.Parallel()

	driver := &Driver{
		model:          "claude-sonnet-4-6",
		maxTokens:      DefaultMaxTokens,
		promptCacheTTL: "5m",
	}
	payload, err := driver.buildMessagesRequest(agent.Request{
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
		}},
		CurrentTurnMessages: []agent.Message{
			{Role: agent.MessageRoleUser, Content: "second question"},
			{Role: agent.MessageRoleUser, Content: "stable protocol"},
			{Role: agent.MessageRoleUser, Content: testCurrentStateMessage(1)},
		},
	})
	if err != nil {
		t.Fatalf("buildMessagesRequest() error = %v", err)
	}
	if len(payload.Messages) != 5 {
		t.Fatalf("len(payload.Messages) = %d, want 5", len(payload.Messages))
	}
	assistantBlocks := mustAnthropicBlocks(t, payload.Messages[1].Content)
	cacheControl, ok := assistantBlocks[0]["cache_control"].(map[string]any)
	if !ok {
		t.Fatalf("payload.Messages[1] = %#v, want cached prior-turn boundary", assistantBlocks)
	}
	if cacheControl["ttl"] != "5m" {
		t.Fatalf("assistant cache_control[ttl] = %#v, want %q", cacheControl["ttl"], "5m")
	}
	if _, ok := payload.Messages[2].Content.(string); !ok {
		t.Fatalf("payload.Messages[2].Content = %#v, want uncached current-turn user string", payload.Messages[2].Content)
	}
}

func TestBuildMessagesRequestReplaysStoredAssistantPreamble(t *testing.T) {
	t.Parallel()

	driver := &Driver{model: "claude-sonnet-4-6", maxTokens: DefaultMaxTokens}
	payload, err := driver.buildMessagesRequest(agent.Request{
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
			"step_1": `[{"type":"thinking","thinking":"","signature":"sig-step-1"},{"type":"text","text":"inspect the file first"}]`,
		},
	})
	if err != nil {
		t.Fatalf("buildMessagesRequest() error = %v", err)
	}

	assistantBlocks := mustAnthropicBlocks(t, payload.Messages[1].Content)
	if len(assistantBlocks) != 3 {
		t.Fatalf("len(assistantBlocks) = %d, want 3", len(assistantBlocks))
	}
	if assistantBlocks[0]["type"] != "thinking" {
		t.Fatalf("assistantBlocks[0] = %#v, want thinking block", assistantBlocks[0])
	}
	if assistantBlocks[0]["signature"] != "sig-step-1" {
		t.Fatalf("assistantBlocks[0][signature] = %#v, want %q", assistantBlocks[0]["signature"], "sig-step-1")
	}
	if assistantBlocks[1]["type"] != "text" || assistantBlocks[1]["text"] != "inspect the file first" {
		t.Fatalf("assistantBlocks[1] = %#v, want stored text block", assistantBlocks[1])
	}
	if assistantBlocks[2]["type"] != "tool_use" || assistantBlocks[2]["id"] != "step_1" || assistantBlocks[2]["name"] != agent.ToolNameReadFile {
		t.Fatalf("assistantBlocks[2] = %#v, want tool_use block", assistantBlocks[2])
	}
}

func TestBuildMessagesRequestReplaysCustomToolWrapperField(t *testing.T) {
	t.Parallel()

	driver := &Driver{model: "claude-sonnet-4-6", maxTokens: DefaultMaxTokens}
	payload, err := driver.buildMessagesRequest(agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleSystem, Content: "test prompt"},
			{Role: agent.MessageRoleUser, Content: "trigger context"},
			{Role: agent.MessageRoleUser, Content: testCurrentStateMessage(2)},
		},
		Steps: []agent.Step{{
			Index:          1,
			ActionName:     agent.ToolNameApplyPatch,
			ActionToolKind: agent.ToolKindCustom,
			ActionInput:    "*** Begin Patch\n*** End Patch",
			Status:         agent.StepStatusOK,
			CWDAfter:       "/workspace",
		}},
		Tools: []agent.ToolDefinition{{
			Name:         agent.ToolNameApplyPatch,
			Kind:         agent.ToolKindCustom,
			CustomFormat: &agent.ToolFormat{Type: "grammar", Syntax: "lark", Definition: "start: PATCH\nPATCH: /.+/s"},
		}},
	})
	if err != nil {
		t.Fatalf("buildMessagesRequest() error = %v", err)
	}
	assistantBlocks := mustAnthropicBlocks(t, payload.Messages[1].Content)
	if len(assistantBlocks) != 1 {
		t.Fatalf("len(assistantBlocks) = %d, want 1", len(assistantBlocks))
	}
	input, ok := assistantBlocks[0]["input"].(map[string]any)
	if !ok {
		t.Fatalf("assistantBlocks[0][input] = %#v, want object", assistantBlocks[0]["input"])
	}
	if input["patch"] != "*** Begin Patch\n*** End Patch" {
		t.Fatalf("custom tool input = %#v, want patch wrapper", input)
	}
}

func TestBuildMessagesRequestReplaysRunShellToolUse(t *testing.T) {
	t.Parallel()

	driver := &Driver{model: "claude-sonnet-4-6", maxTokens: DefaultMaxTokens}
	payload, err := driver.buildMessagesRequest(agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleSystem, Content: "test prompt"},
			{Role: agent.MessageRoleUser, Content: "trigger context"},
			{Role: agent.MessageRoleUser, Content: testCurrentStateMessage(2)},
		},
		Steps: []agent.Step{{
			Index:      1,
			Type:       agent.StepTypeShell,
			ActionName: agent.ToolNameRunShell,
			Shell:      "pwd",
			Status:     agent.StepStatusOK,
			CWDAfter:   "/workspace",
		}},
		Tools: []agent.ToolDefinition{{
			Name:       agent.ToolNameRunShell,
			Kind:       agent.ToolKindFunction,
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}, "required": []string{"command"}, "additionalProperties": false},
		}},
	})
	if err != nil {
		t.Fatalf("buildMessagesRequest() error = %v", err)
	}
	assistantBlocks := mustAnthropicBlocks(t, payload.Messages[1].Content)
	if len(assistantBlocks) != 1 {
		t.Fatalf("len(assistantBlocks) = %d, want 1", len(assistantBlocks))
	}
	if assistantBlocks[0]["type"] != "tool_use" || assistantBlocks[0]["name"] != agent.ToolNameRunShell {
		t.Fatalf("assistantBlocks[0] = %#v, want run_shell tool_use", assistantBlocks[0])
	}
	input, ok := assistantBlocks[0]["input"].(map[string]any)
	if !ok {
		t.Fatalf("assistantBlocks[0][input] = %#v, want object", assistantBlocks[0]["input"])
	}
	if input["command"] != "pwd" {
		t.Fatalf("run_shell input = %#v, want normalized command", input)
	}
}

func TestBuildMessagesRequestMarksErroredReplayToolResult(t *testing.T) {
	t.Parallel()

	driver := &Driver{model: "claude-sonnet-4-6", maxTokens: DefaultMaxTokens}
	payload, err := driver.buildMessagesRequest(agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleSystem, Content: "test prompt"},
			{Role: agent.MessageRoleUser, Content: "trigger context"},
			{Role: agent.MessageRoleUser, Content: testCurrentStateMessage(2)},
		},
		Steps: []agent.Step{{
			Index:          1,
			ActionName:     agent.ToolNameReadFile,
			ActionToolKind: agent.ToolKindFunction,
			ActionInput:    `{"file_path":"/tmp/demo.txt"}`,
			Status:         agent.StepStatusPolicyError,
			Error:          "denied by policy",
			CWDAfter:       "/workspace",
		}},
		Tools: []agent.ToolDefinition{{
			Name:       agent.ToolNameReadFile,
			Kind:       agent.ToolKindFunction,
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"file_path": map[string]any{"type": "string"}}, "required": []string{"file_path"}, "additionalProperties": false},
		}},
	})
	if err != nil {
		t.Fatalf("buildMessagesRequest() error = %v", err)
	}

	userBlocks := mustAnthropicBlocks(t, payload.Messages[2].Content)
	if got := userBlocks[0]["is_error"]; got != true {
		t.Fatalf("userBlocks[0][is_error] = %#v, want true", got)
	}
}

func TestBuildMessagesRequestCachesLastLargeReplayResultWhenPromptCachingEnabled(t *testing.T) {
	t.Parallel()

	driver := &Driver{
		model:          "claude-sonnet-4-6",
		maxTokens:      DefaultMaxTokens,
		promptCacheTTL: "5m",
	}
	largeOutput := strings.Repeat("x", anthropicMinimumCacheableTokens("claude-sonnet-4-6")*4)
	payload, err := driver.buildMessagesRequest(agent.Request{
		Messages: []agent.Message{
			{Role: agent.MessageRoleSystem, Content: "test prompt"},
			{Role: agent.MessageRoleUser, Content: "trigger context"},
			{Role: agent.MessageRoleUser, Content: testCurrentStateMessage(3)},
		},
		Steps: []agent.Step{
			{
				Index:          1,
				ActionName:     agent.ToolNameReadFile,
				ActionToolKind: agent.ToolKindFunction,
				ActionInput:    `{"file_path":"/tmp/one.txt"}`,
				Status:         agent.StepStatusOK,
				CWDAfter:       "/workspace",
				ActionOutput:   largeOutput,
			},
			{
				Index:          2,
				ActionName:     agent.ToolNameGrepFiles,
				ActionToolKind: agent.ToolKindFunction,
				ActionInput:    `{"pattern":"needle","path":"/tmp"}`,
				Status:         agent.StepStatusOK,
				CWDAfter:       "/workspace",
				ActionOutput:   largeOutput,
			},
		},
		Tools: []agent.ToolDefinition{
			{
				Name:       agent.ToolNameReadFile,
				Kind:       agent.ToolKindFunction,
				Parameters: map[string]any{"type": "object", "properties": map[string]any{"file_path": map[string]any{"type": "string"}}, "required": []string{"file_path"}, "additionalProperties": false},
			},
			{
				Name:       agent.ToolNameGrepFiles,
				Kind:       agent.ToolKindFunction,
				Parameters: map[string]any{"type": "object", "properties": map[string]any{"pattern": map[string]any{"type": "string"}}, "required": []string{"pattern"}, "additionalProperties": false},
			},
		},
	})
	if err != nil {
		t.Fatalf("buildMessagesRequest() error = %v", err)
	}
	if len(payload.Messages) != 6 {
		t.Fatalf("len(payload.Messages) = %d, want 6", len(payload.Messages))
	}

	firstResult := mustAnthropicBlocks(t, payload.Messages[2].Content)
	if _, ok := firstResult[0]["cache_control"]; ok {
		t.Fatalf("first replay tool_result = %#v, want no cache_control", firstResult[0])
	}
	secondResult := mustAnthropicBlocks(t, payload.Messages[4].Content)
	cacheControl, ok := secondResult[0]["cache_control"].(map[string]any)
	if !ok {
		t.Fatalf("second replay tool_result = %#v, want cache_control block", secondResult[0])
	}
	if cacheControl["type"] != "ephemeral" {
		t.Fatalf("second replay cache_control[type] = %#v, want ephemeral", cacheControl["type"])
	}
	if cacheControl["ttl"] != "5m" {
		t.Fatalf("second replay cache_control[ttl] = %#v, want %q", cacheControl["ttl"], "5m")
	}
}

func TestAnthropicReplayEligibleForCacheUsesApproxTokenFloor(t *testing.T) {
	t.Parallel()

	if anthropicReplayEligibleForCache(agent.StepReplay{
		ToolName: agent.ToolNameReadFile,
		Output:   strings.Repeat("x", anthropicReplayCacheMinApproxTokens*4-4),
	}, []agent.ToolDefinition{{
		Name:     agent.ToolNameReadFile,
		ReadOnly: true,
	}}, anthropicReplayCacheMinApproxTokens) {
		t.Fatal("anthropicReplayEligibleForCache() = true, want false below approximate token floor")
	}
	if !anthropicReplayEligibleForCache(agent.StepReplay{
		ToolName: agent.ToolNameReadFile,
		Output:   strings.Repeat("x", anthropicReplayCacheMinApproxTokens*4),
	}, []agent.ToolDefinition{{
		Name:     agent.ToolNameReadFile,
		ReadOnly: true,
	}}, anthropicReplayCacheMinApproxTokens) {
		t.Fatal("anthropicReplayEligibleForCache() = false, want true at approximate token floor")
	}
}

func TestParseMessageDecisionPreservesAssistantTextForToolUse(t *testing.T) {
	t.Parallel()

	decision, err := parseMessageDecision(messagesResponse{
		Content: []anthropicContentBlock{
			{
				Type: "text",
				Text: "inspect the config before reading it",
			},
			{
				Type:  "tool_use",
				ID:    "toolu_1",
				Name:  agent.ToolNameReadFile,
				Input: json.RawMessage(`{"file_path":"/tmp/demo.txt"}`),
			},
		},
		StopReason: "tool_use",
	}, []agent.ToolDefinition{{
		Name: agent.ToolNameReadFile,
		Kind: agent.ToolKindFunction,
	}})
	if err != nil {
		t.Fatalf("parseMessageDecision() error = %v", err)
	}
	if decision.Thought != "inspect the config before reading it" {
		t.Fatalf("decision.Thought = %q, want assistant text thought", decision.Thought)
	}
	if decision.Tool == nil || decision.Tool.Name != agent.ToolNameReadFile {
		t.Fatalf("decision.Tool = %#v, want read_file", decision.Tool)
	}
}

func TestParseMessageDecisionPreservesThinkingForToolUse(t *testing.T) {
	t.Parallel()

	decision, err := parseMessageDecision(messagesResponse{
		Content: []anthropicContentBlock{
			{
				Type:     "thinking",
				Thinking: "inspect the config before reading it",
			},
			{
				Type:  "tool_use",
				ID:    "toolu_1",
				Name:  agent.ToolNameReadFile,
				Input: json.RawMessage(`{"file_path":"/tmp/demo.txt"}`),
			},
		},
		StopReason: "tool_use",
	}, []agent.ToolDefinition{{
		Name: agent.ToolNameReadFile,
		Kind: agent.ToolKindFunction,
	}})
	if err != nil {
		t.Fatalf("parseMessageDecision() error = %v", err)
	}
	if decision.Thought != "inspect the config before reading it" {
		t.Fatalf("decision.Thought = %q, want summarized thinking", decision.Thought)
	}
	if decision.Tool == nil || decision.Tool.Name != agent.ToolNameReadFile {
		t.Fatalf("decision.Tool = %#v, want read_file", decision.Tool)
	}
}

func TestParseMessageDecisionMergesThinkingAndTextForToolUse(t *testing.T) {
	t.Parallel()

	decision, err := parseMessageDecision(messagesResponse{
		Content: []anthropicContentBlock{
			{
				Type:     "thinking",
				Thinking: "inspect the config before reading it",
			},
			{
				Type: "text",
				Text: "read the config file next",
			},
			{
				Type:  "tool_use",
				ID:    "toolu_1",
				Name:  agent.ToolNameReadFile,
				Input: json.RawMessage(`{"file_path":"/tmp/demo.txt"}`),
			},
		},
		StopReason: "tool_use",
	}, []agent.ToolDefinition{{
		Name: agent.ToolNameReadFile,
		Kind: agent.ToolKindFunction,
	}})
	if err != nil {
		t.Fatalf("parseMessageDecision() error = %v", err)
	}
	if got := decision.Thought; got != "inspect the config before reading it\nread the config file next" {
		t.Fatalf("decision.Thought = %q, want merged thinking and text", got)
	}
	if decision.Tool == nil || decision.Tool.Name != agent.ToolNameReadFile {
		t.Fatalf("decision.Tool = %#v, want read_file", decision.Tool)
	}
}

func TestParseMessageDecisionCapturesReplayPreambleForToolUse(t *testing.T) {
	t.Parallel()

	decision, err := parseMessageDecision(messagesResponse{
		Content: []anthropicContentBlock{
			{
				Type:      "thinking",
				Thinking:  "",
				Signature: "sig-tool-use",
			},
			{
				Type: "text",
				Text: "inspect the config before reading it",
			},
			{
				Type:  "tool_use",
				ID:    "toolu_1",
				Name:  agent.ToolNameReadFile,
				Input: json.RawMessage(`{"file_path":"/tmp/demo.txt"}`),
			},
		},
		StopReason: "tool_use",
	}, []agent.ToolDefinition{{
		Name: agent.ToolNameReadFile,
		Kind: agent.ToolKindFunction,
	}})
	if err != nil {
		t.Fatalf("parseMessageDecision() error = %v", err)
	}
	if decision.ReplayData == "" {
		t.Fatal("decision.ReplayData = empty, want replay preamble")
	}
	var replayBlocks []map[string]any
	if err := json.Unmarshal([]byte(decision.ReplayData), &replayBlocks); err != nil {
		t.Fatalf("json.Unmarshal(decision.ReplayData): %v", err)
	}
	if len(replayBlocks) != 2 {
		t.Fatalf("len(replayBlocks) = %d, want 2", len(replayBlocks))
	}
	if replayBlocks[0]["type"] != "thinking" || replayBlocks[0]["signature"] != "sig-tool-use" {
		t.Fatalf("replayBlocks[0] = %#v, want preserved thinking block", replayBlocks[0])
	}
	if replayBlocks[1]["type"] != "text" || replayBlocks[1]["text"] != "inspect the config before reading it" {
		t.Fatalf("replayBlocks[1] = %#v, want preserved text block", replayBlocks[1])
	}
}

func TestParseToolUseDecisionExtractsCustomInput(t *testing.T) {
	t.Parallel()

	decision, err := parseToolUseDecision(anthropicContentBlock{
		Type:  "tool_use",
		ID:    "toolu_1",
		Name:  agent.ToolNameApplyPatch,
		Input: json.RawMessage(`{"patch":"*** Begin Patch\n*** End Patch"}`),
	}, []agent.ToolDefinition{{
		Name: agent.ToolNameApplyPatch,
		Kind: agent.ToolKindCustom,
	}})
	if err != nil {
		t.Fatalf("parseToolUseDecision() error = %v", err)
	}
	if decision.Tool == nil || decision.Tool.Kind != agent.ToolKindCustom {
		t.Fatalf("decision.Tool = %#v, want custom apply_patch tool", decision.Tool)
	}
}

func TestParseStrictFinalDecisionDecodesStructuredObject(t *testing.T) {
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

func TestParseStrictFinalDecisionRejectsLegacyFunctionCallJSON(t *testing.T) {
	t.Parallel()

	_, err := parseStrictFinalDecision(`{"type":"function_call","name":"read_file","arguments":"{\"file_path\":\"/tmp/demo.txt\"}","call_id":"call_1"}`)
	if err == nil {
		t.Fatal("parseStrictFinalDecision() error = nil, want legacy wrapper rejection")
	}
}

func TestParseStrictFinalDecisionRejectsLegacyCustomToolCallJSON(t *testing.T) {
	t.Parallel()

	_, err := parseStrictFinalDecision(`{"type":"custom_tool_call","name":"apply_patch","input":"*** Begin Patch\n*** End Patch","call_id":"call_2"}`)
	if err == nil {
		t.Fatal("parseStrictFinalDecision() error = nil, want legacy wrapper rejection")
	}
}

func TestParseStrictFinalDecisionRejectsLegacyLocalShellCallJSON(t *testing.T) {
	t.Parallel()

	_, err := parseStrictFinalDecision(`{"type":"local_shell_call","call_id":"call_3","action":{"type":"exec","command":["bash","-lc","ls -la"],"working_directory":"/workspace","timeout_ms":30000}}`)
	if err == nil {
		t.Fatal("parseStrictFinalDecision() error = nil, want legacy wrapper rejection")
	}
}

func TestParseStrictFinalDecisionRejectsLegacyToolWrapperJSON(t *testing.T) {
	t.Parallel()

	_, err := parseStrictFinalDecision(`{"type":"tool","name":"apply_patch","args":{"patch":"*** Begin Patch\n*** End Patch"},"thought":"patch it"}`)
	if err == nil {
		t.Fatal("parseStrictFinalDecision() error = nil, want legacy wrapper rejection")
	}
}

func TestParseMessageDecisionRejectsTextualFunctionCallFallback(t *testing.T) {
	t.Parallel()

	_, err := parseMessageDecision(messagesResponse{
		Content: []anthropicContentBlock{{
			Type: "text",
			Text: `{"type":"function_call","name":"read_file","arguments":"{\"file_path\":\"/tmp/demo.txt\"}","call_id":"call_1"}`,
		}},
		StopReason: "end_turn",
	}, []agent.ToolDefinition{{
		Name: agent.ToolNameReadFile,
		Kind: agent.ToolKindFunction,
	}})
	if err == nil {
		t.Fatal("parseMessageDecision() error = nil, want strict final rejection")
	}
}

func TestDriverNextCapturesCachedTokensFromUsage(t *testing.T) {
	t.Parallel()

	server, _ := newTestServer(t, []testServerResponse{
		{
			Content: []anthropicContentBlock{{
				Type: "text",
				Text: strictFinalText("done", "done"),
			}},
			StopReason:   "end_turn",
			CachedTokens: 1536,
		},
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
	if decision.Usage.CachedTokens != 1536 {
		t.Fatalf("decision.Usage.CachedTokens = %d, want %d", decision.Usage.CachedTokens, 1536)
	}
}

func TestDriverNextRejectsPlainTextOutsideStrictSchema(t *testing.T) {
	t.Parallel()

	server, _ := newTestServer(t, []testServerResponse{
		{
			Content: []anthropicContentBlock{{
				Type: "text",
				Text: "done",
			}},
			StopReason: "end_turn",
		},
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

func TestDriverNextParsesStructuredFinishThought(t *testing.T) {
	t.Parallel()

	server, _ := newTestServer(t, []testServerResponse{
		{
			Content: []anthropicContentBlock{{
				Type: "text",
				Text: strictFinalText("all work is complete", "done"),
			}},
			StopReason: "end_turn",
		},
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
	if got := decision.Thought; got != "all work is complete" {
		t.Fatalf("decision.Thought = %q, want %q", got, "all work is complete")
	}
	if got := decision.Finish.Value; got != "done" {
		t.Fatalf("decision.Finish.Value = %#v, want %q", got, "done")
	}
}

func TestDriverNextMergesThinkingIntoStructuredFinishThought(t *testing.T) {
	t.Parallel()

	server, _ := newTestServer(t, []testServerResponse{
		{
			Content: []anthropicContentBlock{
				{
					Type:     "thinking",
					Thinking: "inspect the result before finishing",
				},
				{
					Type: "text",
					Text: strictFinalText("all work is complete", "done"),
				},
			},
			StopReason: "end_turn",
		},
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
	if got := decision.Thought; got != "inspect the result before finishing\nall work is complete" {
		t.Fatalf("decision.Thought = %q, want merged final-turn reasoning", got)
	}
	if got := decision.Finish.Value; got != "done" {
		t.Fatalf("decision.Finish.Value = %#v, want %q", got, "done")
	}
}

func TestDriverNextAcceptsStructuredFinishWithoutThought(t *testing.T) {
	t.Parallel()

	server, _ := newTestServer(t, []testServerResponse{
		{
			Content: []anthropicContentBlock{{
				Type: "text",
				Text: `{"result_json":"\"done\""}`,
			}},
			StopReason: "end_turn",
		},
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
	if got := decision.Thought; got != "" {
		t.Fatalf("decision.Thought = %q, want empty thought", got)
	}
	if got := decision.Finish.Value; got != "done" {
		t.Fatalf("decision.Finish.Value = %#v, want %q", got, "done")
	}
}

func TestParseMessageDecisionRejectsMultipleToolUses(t *testing.T) {
	t.Parallel()

	_, err := parseMessageDecision(messagesResponse{
		Content: []anthropicContentBlock{
			{
				Type:  "tool_use",
				ID:    "toolu_1",
				Name:  agent.ToolNameReadFile,
				Input: json.RawMessage(`{"file_path":"/tmp/a"}`),
			},
			{
				Type:  "tool_use",
				ID:    "toolu_2",
				Name:  agent.ToolNameReadFile,
				Input: json.RawMessage(`{"file_path":"/tmp/b"}`),
			},
		},
		StopReason: "tool_use",
	}, []agent.ToolDefinition{{
		Name: agent.ToolNameReadFile,
		Kind: agent.ToolKindFunction,
	}})
	if err == nil {
		t.Fatal("parseMessageDecision() error = nil, want multiple tool use rejection")
	}
	if !strings.Contains(err.Error(), "exactly one tool use") {
		t.Fatalf("parseMessageDecision() error = %v, want multiple tool use rejection", err)
	}
}

func TestRequestMarshalingIncludesDisableParallelToolUse(t *testing.T) {
	t.Parallel()

	body, err := json.Marshal(messagesRequest{
		Model:     "claude-sonnet-4-6",
		MaxTokens: DefaultMaxTokens,
		Messages:  []anthropicMessage{{Role: agent.MessageRoleUser, Content: "hi"}},
		ToolChoice: &anthropicToolChoice{
			Type:                   "auto",
			DisableParallelToolUse: true,
		},
	})
	if err != nil {
		t.Fatalf("json.Marshal(messagesRequest): %v", err)
	}
	if !strings.Contains(string(body), `"tool_choice":{"type":"auto","disable_parallel_tool_use":true}`) {
		t.Fatalf("request JSON = %s, want tool_choice.disable_parallel_tool_use true", string(body))
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(request JSON): %v", err)
	}
	if _, ok := decoded["disable_parallel_tool_use"]; ok {
		t.Fatalf("request JSON = %s, want disable_parallel_tool_use nested under tool_choice only", string(body))
	}
}

type requestRecord struct {
	Request messagesRequest
	Headers http.Header
}

type requestLog struct {
	mu       sync.Mutex
	requests []requestRecord
}

type testServerResponse struct {
	Content      []anthropicContentBlock
	StopReason   string
	CachedTokens int
}

func (l *requestLog) append(req messagesRequest, headers http.Header) {
	l.mu.Lock()
	l.requests = append(l.requests, requestRecord{
		Request: req,
		Headers: headers.Clone(),
	})
	l.mu.Unlock()
}

func (l *requestLog) snapshot() []requestRecord {
	l.mu.Lock()
	defer l.mu.Unlock()

	out := make([]requestRecord, len(l.requests))
	copy(out, l.requests)
	return out
}

func newTestServer(t *testing.T, responses []testServerResponse) (*httptest.Server, *requestLog) {
	t.Helper()

	var (
		mu    sync.Mutex
		index int
	)
	log := &requestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}

		var req messagesRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		log.append(req, r.Header)

		mu.Lock()
		defer mu.Unlock()
		if index >= len(responses) {
			t.Fatalf("received more requests than responses")
		}
		response := responses[index]
		index++
		_ = json.NewEncoder(w).Encode(messagesResponse{
			Content:    response.Content,
			StopReason: response.StopReason,
			Usage: anthropicUsage{
				CacheReadInputTokens: response.CachedTokens,
			},
		})
	}))
	return server, log
}

func newTestDriver(t *testing.T, baseURL string) *Driver {
	t.Helper()

	driver, err := New(Config{
		APIKey:  "test-key",
		BaseURL: baseURL,
		Model:   "claude-sonnet-4-6",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return driver
}

func strictFinalText(thought string, value any) string {
	return agent.StrictFinalResponseExample(thought, value)
}

func mustAnthropicStringContent(t *testing.T, content any) string {
	t.Helper()

	value, ok := content.(string)
	if !ok {
		t.Fatalf("content = %#v, want string", content)
	}
	return value
}

func mustAnthropicTextContent(t *testing.T, content any) string {
	t.Helper()

	if value, ok := content.(string); ok {
		return value
	}
	blocks := mustAnthropicBlocks(t, content)
	if len(blocks) == 0 {
		t.Fatalf("content = %#v, want at least one text block", content)
	}
	text, ok := blocks[0]["text"].(string)
	if !ok {
		t.Fatalf("first block = %#v, want text field", blocks[0])
	}
	return text
}

func mustAnthropicBlocks(t *testing.T, content any) []map[string]any {
	t.Helper()

	data, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("json.Marshal(content): %v", err)
	}
	var blocks []map[string]any
	if err := json.Unmarshal(data, &blocks); err != nil {
		t.Fatalf("json.Unmarshal(blocks): %v", err)
	}
	return blocks
}
