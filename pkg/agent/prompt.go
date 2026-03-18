// See LICENSE for licensing information

package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/richardartoul/swarmd/pkg/sh/moreinterp/coreutils"
	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
)

const DefaultSystemPrompt = `You are a sandboxed local agent.

Use the structured tools provided by the runtime as the source of truth for what actions are available on this turn.

Rules:
- Prefer structured tools over shell whenever a built-in tool fits the task.
- Use at most one tool call per response.
- When tools are available, either emit exactly one tool call or emit no tool call and finish with a structured finish object.
- When a tool is needed, use the runtime's native tool-calling interface instead of writing text JSON wrappers like {"type":"tool"}, {"type":"function_call"}, or {"type":"custom_tool_call"}.
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
- When the task is complete, finish without another tool call by emitting JSON in the form {"type":"finish","thought":"<brief reason for finishing>","result":<user-facing final value>}.
- Keep "thought" concise and put the actual user-facing response or structured return payload in "result".`

const defaultSystemPromptWithNetwork = `You are a sandboxed local agent.

Use the structured tools provided by the runtime as the source of truth for what actions are available on this turn.

Rules:
- Prefer structured tools over shell whenever a built-in tool fits the task.
- Use at most one tool call per response.
- When tools are available, either emit exactly one tool call or emit no tool call and finish with a structured finish object.
- When a tool is needed, use the runtime's native tool-calling interface instead of writing text JSON wrappers like {"type":"tool"}, {"type":"function_call"}, or {"type":"custom_tool_call"}.
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
- When the task is complete, finish without another tool call by emitting JSON in the form {"type":"finish","thought":"<brief reason for finishing>","result":<user-facing final value>}.
- Keep "thought" concise and put the actual user-facing response or structured return payload in "result".`

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

type driverRequestMode int

const (
	driverRequestModeTrigger driverRequestMode = iota
	driverRequestModeSession
)

type driverRequestContext struct {
	Mode          driverRequestMode
	PriorMessages []Message
}

func newTriggerDriverRequestContext() driverRequestContext {
	return driverRequestContext{Mode: driverRequestModeTrigger}
}

func newSessionDriverRequestContext(priorMessages []Message) driverRequestContext {
	return driverRequestContext{
		Mode:          driverRequestModeSession,
		PriorMessages: cloneMessages(priorMessages),
	}
}

func toolAvailabilityPrompt(tools []ToolDefinition, networkEnabled bool, customCommands []sandbox.CommandInfo) string {
	if !hasTool(tools, ToolNameRunShell) {
		return ""
	}

	coreutilsNames := make([]string, 0, len(coreutils.Commands()))
	for _, command := range coreutils.Commands() {
		if command.RequiresNetwork && !networkEnabled {
			continue
		}
		coreutilsNames = append(coreutilsNames, command.Name)
	}
	lines := []string{
		"Runtime-only run_shell guidance:",
		"- Use run_shell only as a sandboxed fallback when no structured tool fits.",
		"- Network access is disabled unless this runtime explicitly exposes network-capable tools or shell commands.",
		"- If you use run_shell, the sandbox command surface is:",
		"- Shell builtins: echo, printf, pwd, cd, set, shift, unset, true, false, break, continue, test, [, :",
		"- Coreutils: "+strings.Join(coreutilsNames, ", "),
		"- Grep policy: this sandbox grep requires grep -F for literal matches or grep -E for regex patterns; plain grep without -E/-F is rejected.",
	}
	if len(customCommands) > 0 {
		lines = append(lines, "- Additional custom sandbox commands available through run_shell:")
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
	return a.buildDriverRequestWithContext(trigger, step, cwd, steps, newTriggerDriverRequestContext())
}

func (a *Agent) buildDriverRequestWithContext(
	trigger Trigger,
	step int,
	cwd string,
	steps []Step,
	requestContext driverRequestContext,
) (Request, string, error) {
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
	req.Messages = append(req.Messages, cloneMessages(requestContext.PriorMessages)...)
	req.Messages = append(req.Messages, Message{
		Role:    MessageRoleUser,
		Content: currentUserTurnContent(prompt, req.Trigger, requestContext.Mode),
	})
	req.Messages = append(req.Messages, Message{
		Role:    MessageRoleUser,
		Content: formatCurrentStateForPromptWithContext(prompt, req, requestContext),
	})
	return req, prompt, nil
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
	return formatCurrentStateForPrompt("", req)
}

func formatCurrentStateForPrompt(prompt string, req Request) string {
	return formatCurrentStateForPromptWithContext(prompt, req, newTriggerDriverRequestContext())
}

func formatCurrentStateForPromptWithContext(prompt string, req Request, requestContext driverRequestContext) string {
	var b strings.Builder
	b.WriteString("Current execution state\n")
	if strings.TrimSpace(req.SandboxRoot) != "" {
		fmt.Fprintf(&b, "Sandbox root: %s\n", req.SandboxRoot)
		b.WriteString("Paths outside the sandbox root are inaccessible.\n")
	}
	fmt.Fprintf(&b, "Current working directory: %s\n", req.CWD)
	fmt.Fprintf(&b, "Current step number: %d\n", req.Step)
	if len(req.Steps) == 0 {
		fmt.Fprintf(&b, "No prior steps have been run for this %s.\n", requestContext.currentStateTarget())
	}
	b.WriteString("\nNative tool metadata is included elsewhere in this request. Retry guidance appears below only when the last attempted tool needs repair.\n")
	if expanded := toolExpandedGuidancePrompt(prompt, req); expanded != "" {
		b.WriteString("\n")
		b.WriteString(expanded)
		b.WriteString("\n")
	}
	b.WriteString("Use exactly one tool call when more work is needed, or no tool call when you are ready to finish with a structured finish object.\n")
	b.WriteString("When finishing, prefer {\"type\":\"finish\",\"thought\":\"...\",\"result\":...} so the runtime can record the final thought separately.\n")
	b.WriteString("Never emit multiple tool calls in a single response.\n")
	return b.String()
}

func currentUserTurnContent(prompt string, trigger Trigger, mode driverRequestMode) string {
	if mode == driverRequestModeSession {
		return prompt
	}
	return formatTriggerContext(prompt, trigger)
}

func (c driverRequestContext) currentStateTarget() string {
	if c.Mode == driverRequestModeSession {
		return "turn"
	}
	return "trigger"
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
	_ = prompt
	tool, ok := selectRetryTool(req)
	if !ok {
		return ""
	}
	return strings.Join([]string{
		"Focused retry guidance for the last failed tool:",
		formatExpandedToolGuidance(tool),
	}, "\n")
}

func selectRetryTool(req Request) (ToolDefinition, bool) {
	retryTool := lastFailedTool(req.Steps)
	if retryTool == "" {
		return ToolDefinition{}, false
	}
	for _, tool := range req.Tools {
		if tool.Name == retryTool {
			return tool, true
		}
	}
	return ToolDefinition{}, false
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
		if strings.TrimSpace(tool.CustomFormat.Definition) != "" {
			fmt.Fprintf(&b, "  Definition:\n%s\n", indentLines(tool.CustomFormat.Definition, "    "))
		}
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
	for idx, example := range limitedToolExamples(tool.Examples, -1) {
		fmt.Fprintf(&b, "  Example %d: %s\n", idx+1, example)
	}
	return strings.TrimRight(b.String(), "\n")
}

func limitedToolExamples(examples []string, limit int) []string {
	if len(examples) == 0 || limit == 0 {
		return nil
	}
	if limit < 0 {
		limit = len(examples)
	}
	trimmed := make([]string, 0, min(limit, len(examples)))
	for _, example := range examples {
		example = strings.TrimSpace(example)
		if example == "" {
			continue
		}
		trimmed = append(trimmed, example)
		if len(trimmed) == limit {
			break
		}
	}
	return trimmed
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

func cloneMessages(messages []Message) []Message {
	return append([]Message(nil), messages...)
}
