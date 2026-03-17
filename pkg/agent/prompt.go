// See LICENSE for licensing information

package agent

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/richardartoul/swarmd/pkg/sh/moreinterp/coreutils"
	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
)

const DefaultSystemPrompt = `You are a sandboxed local agent.

Use the structured tools provided by the runtime as the source of truth for what actions are available on this turn.

Rules:
- Prefer structured tools over shell whenever a built-in tool fits the task.
- Use at most one tool call per response.
- When tools are available, either emit exactly one tool call or emit no tool call and finish normally.
- Never emit multiple tool calls in a single response; do additional tool work in later turns.
- Use run_shell only as a fallback when no structured tool is a good fit.
- Do not invent tools or arguments that are not provided.
- If a tool call fails validation, correct that tool call instead of switching to an unrelated action.
- The shell runs in a sandbox. External commands are blocked.
- Network access is disabled unless network-capable tools are explicitly available.
- If you use run_shell, prefer concise POSIX shell snippets.
- Within run_shell, do not use background jobs, coprocesses, process substitution, exec, or exit.
- Within run_shell, use -- before a literal operand that begins with -.
- Within run_shell, keep options before delegated patterns, scripts, expressions, or subcommands for commands like grep, sed, jq, awk, env, xargs, and find.
- Within run_shell, this sandbox grep requires grep -F for literal matches or grep -E for regex patterns; plain grep without -E or -F is rejected.
- Use the observations from prior steps, including tool inputs and outputs, to decide what to do next.
- When the task is complete, finish by responding normally to the user without another tool call.`

const defaultSystemPromptWithNetwork = `You are a sandboxed local agent.

Use the structured tools provided by the runtime as the source of truth for what actions are available on this turn.

Rules:
- Prefer structured tools over shell whenever a built-in tool fits the task.
- Use at most one tool call per response.
- When tools are available, either emit exactly one tool call or emit no tool call and finish normally.
- Never emit multiple tool calls in a single response; do additional tool work in later turns.
- Use run_shell only as a fallback when no structured tool is a good fit.
- Do not invent tools or arguments that are not provided.
- If a tool call fails validation, correct that tool call instead of switching to an unrelated action.
- The shell runs in a sandbox. External commands are blocked.
- Network-capable tools may be available through the interpreter-owned dialer and HTTP client stack.
- If you use run_shell, prefer concise POSIX shell snippets.
- Within run_shell, do not use background jobs, coprocesses, process substitution, exec, or exit.
- Within run_shell, use -- before a literal operand that begins with -.
- Within run_shell, keep options before delegated patterns, scripts, expressions, or subcommands for commands like grep, sed, jq, awk, env, xargs, and find.
- Within run_shell, this sandbox grep requires grep -F for literal matches or grep -E for regex patterns; plain grep without -E or -F is rejected.
- Use the observations from prior steps, including tool inputs and outputs, to decide what to do next.
- When the task is complete, finish by responding normally to the user without another tool call.`

func defaultSystemPrompt(networkEnabled bool) string {
	if networkEnabled {
		return defaultSystemPromptWithNetwork
	}
	return DefaultSystemPrompt
}

// ComposeSystemPrompt returns the default shell-agent system prompt and appends
// additional agent-specific instructions when provided.
func ComposeSystemPrompt(custom string, networkEnabled bool) string {
	custom = strings.TrimSpace(custom)
	base := defaultSystemPrompt(networkEnabled)
	if custom == "" {
		return base
	}
	return base + "\n\nAdditional agent-specific instructions:\n" + custom
}

// RenderTriggerPrompt normalizes a trigger payload into the prompt text that
// will be embedded in the model request.
func RenderTriggerPrompt(payload any) (string, error) {
	switch payload := payload.(type) {
	case nil:
		return "", nil
	case string:
		return strings.TrimSpace(payload), nil
	case []byte:
		return strings.TrimSpace(string(payload)), nil
	case fmt.Stringer:
		return strings.TrimSpace(payload.String()), nil
	default:
		data, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return "", fmt.Errorf("could not render trigger payload: %w", err)
		}
		return string(data), nil
	}
}

type historyEntry struct {
	Prompt  string
	Summary string
}

func commandInfosFromCustomCommands(commands []sandbox.CustomCommand) []sandbox.CommandInfo {
	if len(commands) == 0 {
		return nil
	}
	infos := make([]sandbox.CommandInfo, 0, len(commands))
	for _, command := range commands {
		infos = append(infos, command.Info)
	}
	return infos
}

func toolAvailabilityPrompt(tools []ToolDefinition, networkEnabled bool, customCommands []sandbox.CommandInfo) string {
	if len(tools) == 0 {
		return ""
	}

	lines := []string{"Available tools for this turn:"}
	for _, tool := range tools {
		line := "- " + tool.Name + ": " + tool.Description
		if tags := formatToolSafetyTags(tool); tags != "" {
			line += " [" + tags + "]"
		}
		if len(tool.RequiredArguments) > 0 {
			line += " Required arguments: " + strings.Join(tool.RequiredArguments, ", ") + "."
		}
		lines = append(lines, line)
	}

	if !hasTool(tools, ToolNameRunShell) {
		return strings.Join(lines, "\n")
	}

	coreutilsNames := make([]string, 0, len(coreutils.Commands()))
	for _, command := range coreutils.Commands() {
		if command.RequiresNetwork && !networkEnabled {
			continue
		}
		coreutilsNames = append(coreutilsNames, command.Name)
	}
	lines = append(lines,
		"",
		"If you use run_shell, the sandbox command surface is:",
		"- Shell builtins: echo, printf, pwd, cd, set, shift, unset, true, false, break, continue, test, [, :",
		"- Coreutils: "+strings.Join(coreutilsNames, ", "),
		"- Grep policy: this sandbox grep requires grep -F for literal matches or grep -E for regex patterns; plain grep without -E/-F is rejected.",
	)
	if len(customCommands) > 0 {
		lines = append(lines, "- Custom commands:")
		for _, command := range customCommands {
			if command.RequiresNetwork && !networkEnabled {
				continue
			}
			line := "  - " + command.Name
			if command.Usage != "" {
				line += ": " + command.Usage
			}
			if command.Description != "" {
				line += " (" + command.Description + ")"
			}
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func (a *Agent) buildDriverRequest(trigger Trigger, step int, cwd string, steps []Step) (Request, string, error) {
	prompt, err := triggerPrompt(trigger)
	if err != nil {
		return Request{}, "", err
	}

	req := Request{
		Trigger:     trigger,
		Step:        step,
		SandboxRoot: a.sandboxRoot,
		CWD:         cwd,
		Steps:       steps,
		Tools:       append([]ToolDefinition(nil), a.toolDefinitions...),
	}
	req.Messages = []Message{
		{Role: MessageRoleSystem, Content: a.systemPrompt},
	}
	if availability := toolAvailabilityPrompt(req.Tools, a.networkEnabled, a.customCommands); availability != "" {
		req.Messages = append(req.Messages, Message{
			Role:    MessageRoleSystem,
			Content: availability,
		})
	}
	if expanded := toolExpandedGuidancePrompt(prompt, req); expanded != "" {
		req.Messages = append(req.Messages, Message{
			Role:    MessageRoleSystem,
			Content: expanded,
		})
	}
	if history := a.historySnapshot(); history != "" {
		req.Messages = append(req.Messages, Message{
			Role:    MessageRoleSystem,
			Content: history,
		})
	}
	req.Messages = append(req.Messages, Message{
		Role:    MessageRoleUser,
		Content: formatTriggerContext(prompt, req.Trigger),
	})
	for _, step := range req.Steps {
		req.Messages = append(req.Messages,
			Message{
				Role:    MessageRoleAssistant,
				Content: formatStepDecision(step),
			},
			Message{
				Role:    MessageRoleUser,
				Content: formatStepObservation(step),
			},
		)
	}
	req.Messages = append(req.Messages, Message{
		Role:    MessageRoleUser,
		Content: formatCurrentState(req),
	})
	return req, prompt, nil
}

func (a *Agent) historySnapshot() string {
	if !a.preserveConversation {
		return ""
	}

	a.historyMu.Lock()
	history := append([]historyEntry(nil), a.history...)
	a.historyMu.Unlock()

	if len(history) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("Conversation history across previous triggers:\n")
	for i, entry := range history {
		fmt.Fprintf(&b, "\nTrigger %d prompt:\n%s\n", i+1, entry.Prompt)
		fmt.Fprintf(&b, "Trigger %d outcome:\n%s\n", i+1, entry.Summary)
	}
	return b.String()
}

func (a *Agent) appendHistory(prompt string, result Result) {
	if !a.preserveConversation {
		return
	}

	var b strings.Builder
	if len(result.Steps) == 0 {
		b.WriteString("Finished without running action steps.\n")
	} else {
		b.WriteString("Action steps run:\n")
		for _, step := range result.Steps {
			label := strings.TrimSpace(step.ActionName)
			if label == "" {
				label = strings.TrimSpace(step.Shell)
			}
			if label == "" {
				label = "step"
			}
			fmt.Fprintf(&b, "%d. %s [%s]\n", step.Index, label, step.Status)
		}
	}
	fmt.Fprintf(&b, "Ending cwd: %s\n", result.CWD)
	if result.Value != nil {
		fmt.Fprintf(&b, "Final result: %s\n", formatValue(result.Value))
	}

	a.historyMu.Lock()
	a.history = append(a.history, historyEntry{
		Prompt:  prompt,
		Summary: strings.TrimSpace(b.String()),
	})
	a.historyMu.Unlock()
}

func triggerPrompt(trigger Trigger) (string, error) {
	return RenderTriggerPrompt(trigger.Payload)
}

func formatTriggerContext(prompt string, trigger Trigger) string {
	var b strings.Builder
	b.WriteString("Trigger context\n")
	fmt.Fprintf(&b, "ID: %s\n", trigger.ID)
	fmt.Fprintf(&b, "Kind: %s\n", trigger.Kind)
	if len(trigger.Metadata) > 0 {
		if data, err := json.MarshalIndent(trigger.Metadata, "", "  "); err == nil {
			fmt.Fprintf(&b, "Metadata:\n%s\n", string(data))
		}
	}
	fmt.Fprintf(&b, "\nUser prompt:\n%s\n", prompt)
	return b.String()
}

func formatCurrentState(req Request) string {
	var b strings.Builder
	b.WriteString("Current execution state\n")
	if strings.TrimSpace(req.SandboxRoot) != "" {
		fmt.Fprintf(&b, "Sandbox root: %s\n", req.SandboxRoot)
		b.WriteString("Paths outside the sandbox root are inaccessible.\n")
	}
	fmt.Fprintf(&b, "Current working directory: %s\n", req.CWD)
	fmt.Fprintf(&b, "Current step number: %d\n", req.Step)
	if len(req.Tools) > 0 {
		names := make([]string, 0, len(req.Tools))
		for _, tool := range req.Tools {
			names = append(names, tool.Name)
		}
		fmt.Fprintf(&b, "Allowed tools this turn: %s\n", strings.Join(names, ", "))
	}
	if len(req.Steps) == 0 {
		b.WriteString("No prior steps have been run for this trigger.\n")
	}
	b.WriteString("\nCompact tool metadata is always included. Focused details appear only for likely relevant or recently failed actions.\n")
	b.WriteString("Use exactly one tool call when more work is needed, or no tool call when you are ready to finish.\n")
	b.WriteString("Never emit multiple tool calls in a single response.\n")
	return b.String()
}

func formatStepDecision(step Step) string {
	name := strings.TrimSpace(step.ActionName)
	if name != "" {
		input := strings.TrimSpace(step.ActionInput)
		if input == "" && step.Shell != "" {
			input = mustCompactJSON(map[string]any{"command": step.Shell})
		}
		callID := stepCallID(step)
		if step.ActionToolKind == ToolKindCustom {
			type priorCustomToolCall struct {
				Type    string `json:"type"`
				Name    string `json:"name"`
				Input   string `json:"input"`
				CallID  string `json:"call_id"`
				Thought string `json:"thought,omitempty"`
			}
			data, err := json.Marshal(priorCustomToolCall{
				Type:    "custom_tool_call",
				Name:    name,
				Input:   input,
				CallID:  callID,
				Thought: step.Thought,
			})
			if err == nil {
				return string(data)
			}
		}
		type priorFunctionCall struct {
			Type      string `json:"type"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			CallID    string `json:"call_id"`
			Thought   string `json:"thought,omitempty"`
		}
		data, err := json.Marshal(priorFunctionCall{
			Type:      "function_call",
			Name:      name,
			Arguments: defaultJSONString(input),
			CallID:    callID,
			Thought:   step.Thought,
		})
		if err == nil {
			return string(data)
		}
	}

	type priorShellDecision struct {
		Type    string `json:"type"`
		Thought string `json:"thought"`
		Shell   string `json:"shell"`
	}

	data, err := json.Marshal(priorShellDecision{
		Type:    "shell",
		Thought: step.Thought,
		Shell:   step.Shell,
	})
	if err != nil {
		return fmt.Sprintf(`{"type":"shell","thought":%q,"shell":%q}`, step.Thought, step.Shell)
	}
	return string(data)
}

func formatStepObservation(step Step) string {
	summary := formatStepObservationSummary(step)
	if step.ActionName == "" {
		return summary
	}
	type toolCallOutput struct {
		Type   string `json:"type"`
		CallID string `json:"call_id"`
		Output string `json:"output"`
	}
	outputType := "function_call_output"
	if step.ActionToolKind == ToolKindCustom {
		outputType = "custom_tool_call_output"
	}
	data, err := json.Marshal(toolCallOutput{
		Type:   outputType,
		CallID: stepCallID(step),
		Output: summary,
	})
	if err != nil {
		return summary
	}
	return string(data)
}

func formatValue(value any) string {
	switch value := value.(type) {
	case string:
		return value
	default:
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return fmt.Sprint(value)
		}
		return string(data)
	}
}

func formatToolSafetyTags(tool ToolDefinition) string {
	if len(tool.SafetyTags) == 0 {
		return ""
	}
	return strings.Join(tool.SafetyTags, ", ")
}

func hasTool(tools []ToolDefinition, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func toolExpandedGuidancePrompt(prompt string, req Request) string {
	selected := selectExpandedTools(prompt, req)
	if len(selected) == 0 {
		return ""
	}
	lines := []string{"Focused tool details for this turn:"}
	for _, tool := range selected {
		lines = append(lines, formatExpandedToolGuidance(tool))
	}
	return strings.Join(lines, "\n")
}

func selectExpandedTools(prompt string, req Request) []ToolDefinition {
	if len(req.Tools) == 0 {
		return nil
	}
	lowerPrompt := strings.ToLower(prompt)
	retryTool := lastFailedTool(req.Steps)
	type scoredTool struct {
		Tool  ToolDefinition
		Score int
	}
	scored := make([]scoredTool, 0, len(req.Tools))
	for _, tool := range req.Tools {
		score := 0
		if retryTool != "" && tool.Name == retryTool {
			score += 100
		}
		if strings.Contains(lowerPrompt, strings.ToLower(tool.Name)) {
			score += 50
		}
		for _, keyword := range toolPromptKeywords(tool.Name) {
			if strings.Contains(lowerPrompt, keyword) {
				score += 10
			}
		}
		if score > 0 {
			scored = append(scored, scoredTool{Tool: tool, Score: score})
		}
	}
	if len(scored) == 0 {
		return nil
	}
	slices.SortFunc(scored, func(left, right scoredTool) int {
		switch {
		case left.Score > right.Score:
			return -1
		case left.Score < right.Score:
			return 1
		default:
			return strings.Compare(left.Tool.Name, right.Tool.Name)
		}
	})
	limit := min(3, len(scored))
	selected := make([]ToolDefinition, 0, limit)
	for _, item := range scored[:limit] {
		selected = append(selected, item.Tool)
	}
	return selected
}

func lastFailedTool(steps []Step) string {
	for idx := len(steps) - 1; idx >= 0; idx-- {
		step := steps[idx]
		if step.ActionName == "" {
			continue
		}
		switch step.Status {
		case StepStatusParseError, StepStatusPolicyError:
			return step.ActionName
		}
	}
	return ""
}

func toolPromptKeywords(name string) []string {
	switch name {
	case ToolNameListDir:
		return []string{"directory", "folder", "list", "tree"}
	case ToolNameReadFile:
		return []string{"file", "read", "lines", "content"}
	case ToolNameGrepFiles:
		return []string{"grep", "pattern", "search files", "regex"}
	case ToolNameWebSearch:
		return []string{"web", "search", "internet", "search engine"}
	case ToolNameReadWebPage:
		return []string{"page", "url", "website", "html", "markdown"}
	case ToolNameHTTPRequest:
		return []string{"http", "https", "api", "request", "headers", "json"}
	case ToolNameApplyPatch:
		return []string{"patch", "edit", "modify", "update", "change"}
	case ToolNameRunShell:
		return []string{"shell", "bash", "terminal", "command"}
	default:
		return nil
	}
}

func formatExpandedToolGuidance(tool ToolDefinition) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- %s\n", tool.Name)
	fmt.Fprintf(&b, "  Description: %s\n", tool.Description)
	if len(tool.RequiredArguments) > 0 {
		fmt.Fprintf(&b, "  Required arguments: %s\n", strings.Join(tool.RequiredArguments, ", "))
	}
	switch {
	case tool.CustomFormat != nil:
		fmt.Fprintf(&b, "  Format: %s/%s\n", tool.CustomFormat.Type, tool.CustomFormat.Syntax)
	default:
		if len(tool.Parameters) > 0 {
			if schema, err := json.MarshalIndent(tool.Parameters, "", "  "); err == nil {
				fmt.Fprintf(&b, "  Schema:\n%s\n", indentLines(string(schema), "    "))
			}
		}
	}
	if tool.OutputNotes != "" {
		fmt.Fprintf(&b, "  Output notes: %s\n", tool.OutputNotes)
	}
	for idx, example := range tool.Examples {
		fmt.Fprintf(&b, "  Example %d: %s\n", idx+1, example)
	}
	return strings.TrimRight(b.String(), "\n")
}

func indentLines(text, prefix string) string {
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	for idx, line := range lines {
		lines[idx] = prefix + line
	}
	return strings.Join(lines, "\n")
}

func stepCallID(step Step) string {
	return fmt.Sprintf("step_%d", step.Index)
}

func defaultJSONString(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return "{}"
	}
	return input
}

func formatStepObservationSummary(step Step) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Observation for step %d\n", step.Index)
	if step.ActionName != "" {
		fmt.Fprintf(&b, "Action: %s\n", step.ActionName)
	}
	fmt.Fprintf(&b, "Status: %s\n", step.Status)
	if step.ExitStatus != 0 {
		fmt.Fprintf(&b, "Exit status: %d\n", step.ExitStatus)
	}
	if step.Error != "" {
		fmt.Fprintf(&b, "Error: %s\n", step.Error)
	}
	fmt.Fprintf(&b, "Working directory after step: %s\n", step.CWDAfter)
	if step.ActionOutput != "" {
		fmt.Fprintf(&b, "Output:\n%s\n", step.ActionOutput)
	}
	if step.ActionOutputTruncated {
		b.WriteString("Output was truncated.\n")
	}
	if step.Stdout != "" {
		fmt.Fprintf(&b, "Stdout:\n%s\n", step.Stdout)
	}
	if step.StdoutTruncated {
		b.WriteString("Stdout was truncated.\n")
	}
	if step.Stderr != "" {
		fmt.Fprintf(&b, "Stderr:\n%s\n", step.Stderr)
	}
	if step.StderrTruncated {
		b.WriteString("Stderr was truncated.\n")
	}
	return b.String()
}
