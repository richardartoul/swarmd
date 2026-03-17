package anthropic

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

	if _, err := New(Config{Model: "claude-sonnet-4-6"}); err == nil {
		t.Fatal("New() error = nil, want missing api key error")
	}
	if _, err := New(Config{APIKey: "test-key"}); err == nil {
		t.Fatal("New() error = nil, want missing model error")
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
	if _, err := New(Config{
		APIKey:               "test-key",
		Model:                "claude-sonnet-4-6",
		PreserveConversation: true,
	}); err == nil {
		t.Fatal("New() error = nil, want preserve conversation rejection")
	}
}

func TestDriverNextMovesSystemMessagesIntoTopLevelSystem(t *testing.T) {
	t.Parallel()

	server, requests := newTestServer(t, []testServerResponse{
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
	if snapshot[0].Request.System != "test prompt\n\ntool availability" {
		t.Fatalf("request system = %q, want joined system prompt", snapshot[0].Request.System)
	}
	if snapshot[0].Request.MaxTokens != DefaultMaxTokens {
		t.Fatalf("request max_tokens = %d, want %d", snapshot[0].Request.MaxTokens, DefaultMaxTokens)
	}
	if len(snapshot[0].Request.Messages) != 2 {
		t.Fatalf("len(request messages) = %d, want 2", len(snapshot[0].Request.Messages))
	}
	if snapshot[0].Request.Messages[0].Role != agent.MessageRoleUser || snapshot[0].Request.Messages[0].Content != "say done" {
		t.Fatalf("first request message = %#v, want user message", snapshot[0].Request.Messages[0])
	}
	if snapshot[0].Request.Messages[1].Role != agent.MessageRoleAssistant || snapshot[0].Request.Messages[1].Content != "previous step" {
		t.Fatalf("second request message = %#v, want assistant message", snapshot[0].Request.Messages[1])
	}
	if got := snapshot[0].Headers.Get("x-api-key"); got != "test-key" {
		t.Fatalf("x-api-key header = %q, want %q", got, "test-key")
	}
	if got := snapshot[0].Headers.Get("anthropic-version"); got != anthropicVersion {
		t.Fatalf("anthropic-version header = %q, want %q", got, anthropicVersion)
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
			},
			{
				Name:         agent.ToolNameApplyPatch,
				Description:  "Apply a structured patch to local files.",
				Kind:         agent.ToolKindCustom,
				CustomFormat: &agent.ToolFormat{Type: "grammar", Syntax: "lark", Definition: "start: PATCH\nPATCH: /.+/s"},
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
	if !snapshot[0].Request.DisableParallelToolUse {
		t.Fatal("request disable_parallel_tool_use = false, want true")
	}
	if len(snapshot[0].Request.Tools) != 2 {
		t.Fatalf("len(request tools) = %d, want 2", len(snapshot[0].Request.Tools))
	}
	if snapshot[0].Request.Tools[0].Name != agent.ToolNameReadFile {
		t.Fatalf("first tool = %#v, want read_file", snapshot[0].Request.Tools[0])
	}
	patchSchema := snapshot[0].Request.Tools[1].InputSchema
	if snapshot[0].Request.Tools[1].Name != agent.ToolNameApplyPatch {
		t.Fatalf("second tool = %#v, want apply_patch", snapshot[0].Request.Tools[1])
	}
	properties, ok := patchSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("patch schema properties = %#v, want object", patchSchema["properties"])
	}
	if _, ok := properties["patch"]; !ok {
		t.Fatalf("patch schema = %#v, want patch wrapper property", patchSchema)
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

func TestDriverNextCapturesCachedTokensFromUsage(t *testing.T) {
	t.Parallel()

	server, _ := newTestServer(t, []testServerResponse{
		{
			Content: []anthropicContentBlock{{
				Type: "text",
				Text: "done",
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

func TestDriverNextTreatsTextAsFinish(t *testing.T) {
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
		Model:                  "claude-sonnet-4-6",
		MaxTokens:              DefaultMaxTokens,
		Messages:               []anthropicMessage{{Role: agent.MessageRoleUser, Content: "hi"}},
		ToolChoice:             &anthropicToolChoice{Type: "auto"},
		DisableParallelToolUse: true,
	})
	if err != nil {
		t.Fatalf("json.Marshal(messagesRequest): %v", err)
	}
	if !strings.Contains(string(body), `"disable_parallel_tool_use":true`) {
		t.Fatalf("request JSON = %s, want explicit disable_parallel_tool_use true", string(body))
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
