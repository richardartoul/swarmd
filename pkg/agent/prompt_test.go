package agent

import (
	"strings"
	"testing"

	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
)

func TestComposeSystemPromptWithoutCustomPrompt(t *testing.T) {
	t.Parallel()

	got := ComposeSystemPrompt("", false)
	if got != DefaultSystemPrompt {
		t.Fatalf("ComposeSystemPrompt() = %q, want DefaultSystemPrompt", got)
	}
}

func TestComposeSystemPromptAppendsCustomPrompt(t *testing.T) {
	t.Parallel()

	got := ComposeSystemPrompt("Run curl and summarize the result.", true)
	if !strings.Contains(got, `Use the structured tools provided by the runtime as the source of truth`) {
		t.Fatalf("ComposeSystemPrompt() = %q, want tool-aware base instructions", got)
	}
	if !strings.Contains(got, `Use run_shell only as a fallback when no structured tool is a good fit.`) {
		t.Fatalf("ComposeSystemPrompt() = %q, want run_shell fallback guidance", got)
	}
	if !strings.Contains(got, `Additional agent-specific instructions:`) {
		t.Fatalf("ComposeSystemPrompt() = %q, want custom prompt heading", got)
	}
	if !strings.Contains(got, `Run curl and summarize the result.`) {
		t.Fatalf("ComposeSystemPrompt() = %q, want appended custom prompt", got)
	}
	if !strings.Contains(got, `Network-capable tools may be available through the interpreter-owned dialer`) {
		t.Fatalf("ComposeSystemPrompt() = %q, want network-aware base prompt", got)
	}
}

func TestComposeSystemPromptIncludesCommandShapeGuidance(t *testing.T) {
	t.Parallel()

	got := ComposeSystemPrompt("", true)
	if !strings.Contains(got, `use -- before a literal operand that begins with -.`) {
		t.Fatalf("ComposeSystemPrompt() = %q, want literal operand guidance", got)
	}
	if !strings.Contains(got, `commands like grep, sed, jq, awk, env, xargs, and find`) {
		t.Fatalf("ComposeSystemPrompt() = %q, want boundary-sensitive command guidance", got)
	}
	if !strings.Contains(got, `Use at most one tool call per response.`) {
		t.Fatalf("ComposeSystemPrompt() = %q, want tool-call limit guidance", got)
	}
	if !strings.Contains(got, `either emit exactly one tool call or emit no tool call and finish with a structured finish object.`) {
		t.Fatalf("ComposeSystemPrompt() = %q, want explicit single-tool-call guidance", got)
	}
	if !strings.Contains(got, `Never emit multiple tool calls in a single response; do additional tool work in later turns.`) {
		t.Fatalf("ComposeSystemPrompt() = %q, want no-multiple-tool-calls guidance", got)
	}
	if !strings.Contains(got, `use the runtime's native tool-calling interface instead of writing text JSON wrappers`) {
		t.Fatalf("ComposeSystemPrompt() = %q, want native tool-call guidance", got)
	}
	if !strings.Contains(got, `{"type":"finish","thought":"<brief reason for finishing>","result":<user-facing final value>}`) {
		t.Fatalf("ComposeSystemPrompt() = %q, want structured finish guidance", got)
	}
}

func TestFormatCurrentStateIncludesSingleToolCallReminder(t *testing.T) {
	t.Parallel()

	got := formatCurrentState(Request{
		CWD:  "/workspace",
		Step: 1,
	})
	if !strings.Contains(got, `Use exactly one tool call when more work is needed, or no tool call when you are ready to finish with a structured finish object.`) {
		t.Fatalf("formatCurrentState() = %q, want explicit single-tool-call reminder", got)
	}
	if !strings.Contains(got, `{"type":"finish","thought":"...","result":...}`) {
		t.Fatalf("formatCurrentState() = %q, want structured finish reminder", got)
	}
	if !strings.Contains(got, `Never emit multiple tool calls in a single response.`) {
		t.Fatalf("formatCurrentState() = %q, want no-multiple-tool-calls reminder", got)
	}
}

func TestFormatCurrentStateIncludesSandboxRootGuidance(t *testing.T) {
	t.Parallel()

	got := formatCurrentState(Request{
		SandboxRoot: "/workspace",
		CWD:         "/workspace/demo",
		Step:        2,
	})
	if !strings.Contains(got, "Sandbox root: /workspace") {
		t.Fatalf("formatCurrentState() = %q, want sandbox root line", got)
	}
	if !strings.Contains(got, "Paths outside the sandbox root are inaccessible.") {
		t.Fatalf("formatCurrentState() = %q, want sandbox boundary guidance", got)
	}
	if !strings.Contains(got, "Current working directory: /workspace/demo") {
		t.Fatalf("formatCurrentState() = %q, want cwd line", got)
	}
}

func TestToolAvailabilityPromptIncludesToolsAndRunShellCommands(t *testing.T) {
	t.Parallel()

	got := toolAvailabilityPrompt([]ToolDefinition{
		{
			Name:              ToolNameReadFile,
			Description:       "Reads a local file with bounded output.",
			RequiredArguments: []string{"file_path"},
			SafetyTags:        []string{"read_only"},
			ReadOnly:          true,
		},
		{
			Name:              ToolNameRunShell,
			Description:       "Runs one sandboxed shell command when no structured tool fits.",
			RequiredArguments: []string{"command"},
			SafetyTags:        []string{"fallback"},
			Fallback:          true,
		},
	}, true, []sandbox.CommandInfo{{
		Name:        "server_log",
		Usage:       "server_log --level <debug|info|warn|error> <message...>",
		Description: "write a message to the server logs",
	}})
	if !strings.Contains(got, "Runtime-only run_shell guidance:") {
		t.Fatalf("toolAvailabilityPrompt() = %q, want runtime-only heading", got)
	}
	if strings.Contains(got, "read_file: Reads a local file with bounded output.") {
		t.Fatalf("toolAvailabilityPrompt() = %q, want native tool catalog omitted", got)
	}
	if !strings.Contains(got, "If you use run_shell, the sandbox command surface is:") {
		t.Fatalf("toolAvailabilityPrompt() = %q, want shell command guidance", got)
	}
	if !strings.Contains(got, "Use run_shell only as a sandboxed fallback when no structured tool fits.") {
		t.Fatalf("toolAvailabilityPrompt() = %q, want explicit fallback reminder", got)
	}
	if !strings.Contains(got, "Coreutils:") {
		t.Fatalf("toolAvailabilityPrompt() = %q, want coreutils list", got)
	}
	if !strings.Contains(got, "test, [") {
		t.Fatalf("toolAvailabilityPrompt() = %q, want supported test builtins", got)
	}
	if !strings.Contains(got, "curl") {
		t.Fatalf("toolAvailabilityPrompt() = %q, want network-enabled curl", got)
	}
	if !strings.Contains(got, "grep -F") || !strings.Contains(got, "grep -E") {
		t.Fatalf("toolAvailabilityPrompt() = %q, want grep guidance", got)
	}
	if !strings.Contains(got, "plain grep without -E/-F is rejected") {
		t.Fatalf("toolAvailabilityPrompt() = %q, want grep policy warning", got)
	}
	if !strings.Contains(got, "server_log --level <debug|info|warn|error> <message...>") {
		t.Fatalf("toolAvailabilityPrompt() = %q, want custom command usage", got)
	}
}

func TestToolAvailabilityPromptHidesRunShellSurfaceWhenRunShellUnavailable(t *testing.T) {
	t.Parallel()

	got := toolAvailabilityPrompt([]ToolDefinition{{
		Name:              ToolNameReadFile,
		Description:       "Reads a local file with bounded output.",
		RequiredArguments: []string{"file_path"},
		SafetyTags:        []string{"read_only"},
	}}, false, []sandbox.CommandInfo{
		{
			Name:            "slack",
			Usage:           "slack <post|replies> [options]",
			Description:     "post to Slack and fetch replies",
			RequiresNetwork: true,
		},
		{
			Name:        "server_log",
			Usage:       "server_log --level <debug|info|warn|error> <message...>",
			Description: "write a message to the server logs",
		},
	})
	if got != "" {
		t.Fatalf("toolAvailabilityPrompt() = %q, want no run_shell guidance", got)
	}
}

func TestToolExpandedGuidancePromptSkipsRoutineTurns(t *testing.T) {
	t.Parallel()

	req := Request{
		Tools: []ToolDefinition{
			builtInToolDefinitions[ToolNameApplyPatch],
			builtInToolDefinitions[ToolNameReadFile],
		},
	}
	got := toolExpandedGuidancePrompt("patch the file carefully", req)
	if got != "" {
		t.Fatalf("toolExpandedGuidancePrompt() = %q, want routine turns to rely on native tool metadata", got)
	}
}

func TestToolExpandedGuidancePromptKeepsFullApplyPatchGuidanceOnRetry(t *testing.T) {
	t.Parallel()

	req := Request{
		Tools: []ToolDefinition{
			builtInToolDefinitions[ToolNameApplyPatch],
		},
		Steps: []Step{{
			Index:      1,
			ActionName: ToolNameApplyPatch,
			Status:     StepStatusPolicyError,
			Error:      "patch did not match grammar",
		}},
	}
	got := toolExpandedGuidancePrompt("try again", req)
	if !strings.Contains(got, "- apply_patch") {
		t.Fatalf("toolExpandedGuidancePrompt() = %q, want apply_patch details", got)
	}
	if !strings.Contains(got, "Focused retry guidance for the last failed tool:") {
		t.Fatalf("toolExpandedGuidancePrompt() = %q, want retry heading", got)
	}
	if !strings.Contains(got, "Definition:\n    start: begin_patch hunk end_patch") {
		t.Fatalf("toolExpandedGuidancePrompt() = %q, want full custom format definition on retry", got)
	}
}

func TestToolExpandedGuidancePromptExpandsLastFailedTool(t *testing.T) {
	t.Parallel()

	req := Request{
		Tools: []ToolDefinition{
			builtInToolDefinitions[ToolNameHTTPRequest],
		},
		Steps: []Step{{
			Index:      1,
			ActionName: ToolNameHTTPRequest,
			Status:     StepStatusPolicyError,
			Error:      "url must not be empty",
		}},
	}
	got := toolExpandedGuidancePrompt("try again", req)
	if !strings.Contains(got, "- http_request") {
		t.Fatalf("toolExpandedGuidancePrompt() = %q, want failed tool details", got)
	}
	if !strings.Contains(got, "Schema:") {
		t.Fatalf("toolExpandedGuidancePrompt() = %q, want schema details", got)
	}
}

func TestToolExpandedGuidancePromptOmitsUnknownFailedTool(t *testing.T) {
	t.Parallel()

	req := Request{
		Tools: []ToolDefinition{
			builtInToolDefinitions[ToolNameReadFile],
		},
		Steps: []Step{{
			Index:      1,
			ActionName: ToolNameApplyPatch,
			Status:     StepStatusPolicyError,
		}},
	}
	got := toolExpandedGuidancePrompt("try again", req)
	if got != "" {
		t.Fatalf("toolExpandedGuidancePrompt() = %q, want missing tool to suppress retry guidance", got)
	}
}

func TestFormatExpandedToolGuidanceIncludesAllRetryExamples(t *testing.T) {
	t.Parallel()

	tool := ToolDefinition{
		Name:         ToolNameApplyPatch,
		Description:  "Apply a structured patch.",
		Kind:         ToolKindCustom,
		CustomFormat: &ToolFormat{Type: "grammar", Syntax: "lark", Definition: "start: PATCH\nPATCH: /.+/s"},
		Examples: []string{
			"first example",
			"second example",
		},
	}
	got := formatExpandedToolGuidance(tool)
	if !strings.Contains(got, "Example 1: first example") {
		t.Fatalf("formatExpandedToolGuidance() = %q, want first example", got)
	}
	if !strings.Contains(got, "Example 2: second example") {
		t.Fatalf("formatExpandedToolGuidance() = %q, want all retry examples", got)
	}
}

func TestFormatCurrentStateForPromptIncludesRetryGuidance(t *testing.T) {
	t.Parallel()

	got := formatCurrentStateForPrompt("patch the file carefully", Request{
		CWD:  "/workspace",
		Step: 2,
		Tools: []ToolDefinition{
			builtInToolDefinitions[ToolNameApplyPatch],
			builtInToolDefinitions[ToolNameReadFile],
		},
		Steps: []Step{{
			Index:      1,
			ActionName: ToolNameApplyPatch,
			Status:     StepStatusPolicyError,
		}},
	})
	if !strings.Contains(got, "Focused retry guidance for the last failed tool:") {
		t.Fatalf("formatCurrentStateForPrompt() = %q, want retry tool detail heading", got)
	}
	if !strings.Contains(got, "- apply_patch") {
		t.Fatalf("formatCurrentStateForPrompt() = %q, want apply_patch details", got)
	}
}

func TestBuildDriverRequestPlacesFocusedToolGuidanceInCurrentStateMessage(t *testing.T) {
	t.Parallel()

	a := &Agent{
		sandboxRoot:  "/workspace",
		systemPrompt: DefaultSystemPrompt,
		toolDefinitions: []ToolDefinition{
			builtInToolDefinitions[ToolNameApplyPatch],
			builtInToolDefinitions[ToolNameReadFile],
		},
	}
	req, _, err := a.buildDriverRequest(Trigger{
		ID:      "trigger-1",
		Kind:    "user",
		Payload: "patch the file carefully",
	}, 2, "/workspace", []Step{{
		Index:      1,
		ActionName: ToolNameApplyPatch,
		Status:     StepStatusPolicyError,
	}})
	if err != nil {
		t.Fatalf("buildDriverRequest() error = %v", err)
	}

	for idx, message := range req.Messages {
		if message.Role == MessageRoleSystem && strings.Contains(message.Content, "Focused retry guidance for the last failed tool:") {
			t.Fatalf("req.Messages[%d] = %#v, want focused tool guidance outside system messages", idx, message)
		}
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role != MessageRoleUser {
		t.Fatalf("last message role = %q, want user", last.Role)
	}
	if !strings.Contains(last.Content, "Focused retry guidance for the last failed tool:") {
		t.Fatalf("last message = %#v, want focused tool guidance in current-state message", last)
	}
	if !strings.Contains(last.Content, "- apply_patch") {
		t.Fatalf("last message = %#v, want apply_patch details in current-state message", last)
	}
}

func TestBuildDriverRequestWithSessionContextUsesPlainConversationTurns(t *testing.T) {
	t.Parallel()

	a := &Agent{
		sandboxRoot:  "/workspace",
		systemPrompt: DefaultSystemPrompt,
	}
	req, _, err := a.buildDriverRequestWithContext(Trigger{
		ID:      "trigger-2",
		Kind:    "repl.prompt",
		Payload: "second question",
	}, 1, "/workspace", nil, newSessionDriverRequestContext([]Message{
		{Role: MessageRoleUser, Content: "first question"},
		{Role: MessageRoleAssistant, Content: "first answer"},
	}))
	if err != nil {
		t.Fatalf("buildDriverRequestWithContext() error = %v", err)
	}

	if len(req.Messages) != 5 {
		t.Fatalf("len(req.Messages) = %d, want 5", len(req.Messages))
	}
	if req.Messages[1] != (Message{Role: MessageRoleUser, Content: "first question"}) {
		t.Fatalf("req.Messages[1] = %#v, want prior user turn", req.Messages[1])
	}
	if req.Messages[2] != (Message{Role: MessageRoleAssistant, Content: "first answer"}) {
		t.Fatalf("req.Messages[2] = %#v, want prior assistant turn", req.Messages[2])
	}
	if req.Messages[3] != (Message{Role: MessageRoleUser, Content: "second question"}) {
		t.Fatalf("req.Messages[3] = %#v, want plain current user turn", req.Messages[3])
	}
	if strings.Contains(req.Messages[3].Content, "Trigger context") {
		t.Fatalf("current session user message = %#v, want plain prompt without trigger wrapper", req.Messages[3])
	}
	if !strings.Contains(req.Messages[4].Content, "No prior steps have been run for this turn.") {
		t.Fatalf("current-state message = %#v, want turn-scoped footer wording", req.Messages[4])
	}
}

func TestReadFileNestedSchemaUsesEmptyRequiredArray(t *testing.T) {
	t.Parallel()

	tool := builtInToolDefinitions[ToolNameReadFile]
	indentation, ok := tool.Parameters["properties"].(map[string]any)["indentation"].(map[string]any)
	if !ok {
		t.Fatalf("read_file indentation schema missing or wrong type: %#v", tool.Parameters)
	}
	required, ok := indentation["required"].([]string)
	if !ok {
		t.Fatalf("indentation required = %#v, want []string", indentation["required"])
	}
	if len(required) != 0 {
		t.Fatalf("len(indentation required) = %d, want 0", len(required))
	}
}

func TestReadFileModeSchemaEnumeratesSupportedValues(t *testing.T) {
	t.Parallel()

	tool := builtInToolDefinitions[ToolNameReadFile]
	mode, ok := tool.Parameters["properties"].(map[string]any)["mode"].(map[string]any)
	if !ok {
		t.Fatalf("read_file mode schema missing or wrong type: %#v", tool.Parameters)
	}
	enumValues, ok := mode["enum"].([]string)
	if !ok {
		t.Fatalf("mode enum = %#v, want []string", mode["enum"])
	}
	if len(enumValues) != 2 || enumValues[0] != "slice" || enumValues[1] != "indentation" {
		t.Fatalf("mode enum = %#v, want [slice indentation]", enumValues)
	}
}
