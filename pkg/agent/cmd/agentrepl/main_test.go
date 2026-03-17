package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/richardartoul/swarmd/pkg/agent"
	"github.com/richardartoul/swarmd/pkg/sh/interp"
)

func TestVerboseDriverPrintsThoughtAndShell(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	driver := verboseDriver{
		next: agent.DriverFunc(func(ctx context.Context, req agent.Request) (agent.Decision, error) {
			return agent.Decision{
				Thought: "inspect the directory",
				Shell:   &agent.ShellAction{Source: "ls -la"},
			}, nil
		}),
		stdout: &stdout,
	}

	decision, err := driver.Next(context.Background(), agent.Request{Step: 2})
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if decision.Shell == nil || decision.Shell.Source != "ls -la" {
		t.Fatalf("decision.Shell = %#v, want ls -la", decision.Shell)
	}

	got := stdout.String()
	if !strings.Contains(got, "step 2 thinking> inspect the directory") {
		t.Fatalf("stdout = %q, want thought line", got)
	}
	if !strings.Contains(got, "step 2 running> ls -la") {
		t.Fatalf("stdout = %q, want running line", got)
	}
}

func TestVerboseDriverPrintsToolDecision(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	driver := verboseDriver{
		next: agent.DriverFunc(func(ctx context.Context, req agent.Request) (agent.Decision, error) {
			return agent.Decision{
				Thought: "read the config file",
				Tool: &agent.ToolAction{
					Name:  agent.ToolNameReadFile,
					Kind:  agent.ToolKindFunction,
					Input: `{"file_path":"/tmp/config.yaml"}`,
				},
			}, nil
		}),
		stdout: &stdout,
	}

	decision, err := driver.Next(context.Background(), agent.Request{Step: 3})
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if decision.Tool == nil || decision.Tool.Name != agent.ToolNameReadFile {
		t.Fatalf("decision.Tool = %#v, want read_file", decision.Tool)
	}

	got := stdout.String()
	if !strings.Contains(got, "step 3 tool> read_file") {
		t.Fatalf("stdout = %q, want tool line", got)
	}
	if !strings.Contains(got, `step 3 input> {"file_path":"/tmp/config.yaml"}`) {
		t.Fatalf("stdout = %q, want tool input line", got)
	}
}

func TestRuntimeOptionsAgentConfigEnablesFullCapabilities(t *testing.T) {
	t.Parallel()

	opts := runtimeOptions{
		rootDir:         t.TempDir(),
		networkDialer:   interp.OSNetworkDialer{},
		maxSteps:        agent.DefaultMaxSteps,
		maxOutputBytes:  agent.DefaultMaxOutputBytes,
		rememberHistory: true,
		preserveState:   true,
	}

	cfg, err := opts.agentConfig(nil, agent.DriverFunc(func(context.Context, agent.Request) (agent.Decision, error) {
		return agent.Decision{Finish: &agent.FinishAction{Value: "done"}}, nil
	}), nil, nil, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("agentConfig() error = %v", err)
	}
	if !cfg.NetworkEnabled {
		t.Fatal("cfg.NetworkEnabled = false, want true")
	}
	if len(cfg.ConfiguredTools) != 0 {
		t.Fatalf("cfg.ConfiguredTools = %#v, want no explicit custom tools", cfg.ConfiguredTools)
	}
	if got, want := cfg.SystemPrompt, agent.ComposeSystemPrompt("", true); got != want {
		t.Fatalf("cfg.SystemPrompt = %q, want %q", got, want)
	}
}

func TestRuntimeOptionsAgentConfigAppendsSystemPromptFileInstructions(t *testing.T) {
	t.Parallel()

	opts := runtimeOptions{
		rootDir:         t.TempDir(),
		systemPrompt:    "Prioritize deep debugging over speed.",
		maxSteps:        agent.DefaultMaxSteps,
		maxOutputBytes:  agent.DefaultMaxOutputBytes,
		rememberHistory: true,
		preserveState:   true,
	}

	cfg, err := opts.agentConfig(nil, agent.DriverFunc(func(context.Context, agent.Request) (agent.Decision, error) {
		return agent.Decision{Finish: &agent.FinishAction{Value: "done"}}, nil
	}), nil, nil, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("agentConfig() error = %v", err)
	}
	if got, want := cfg.SystemPrompt, agent.ComposeSystemPrompt(opts.systemPrompt, false); got != want {
		t.Fatalf("cfg.SystemPrompt = %q, want %q", got, want)
	}
}

func TestAgentREPLUsesPermissiveDebugDefaults(t *testing.T) {
	t.Parallel()

	if defaultREPLMaxSteps != 1000 {
		t.Fatalf("defaultREPLMaxSteps = %d, want 1000", defaultREPLMaxSteps)
	}
	if defaultREPLStepTimeout != 5*time.Minute {
		t.Fatalf("defaultREPLStepTimeout = %v, want 5m", defaultREPLStepTimeout)
	}
}

func TestProgressPrinterPrintsStepAndResult(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	printer := progressPrinter{
		stdout:  &stdout,
		stderr:  &stderr,
		verbose: true,
	}

	if err := printer.HandleStep(context.Background(), agent.Trigger{ID: "t1"}, agent.Step{
		Index:        1,
		Status:       agent.StepStatusExitStatus,
		ExitStatus:   1,
		ActionOutput: "matched file.txt",
		Error:        "command failed",
	}); err != nil {
		t.Fatalf("HandleStep() error = %v", err)
	}
	if err := printer.HandleResult(context.Background(), agent.Result{
		Status: agent.ResultStatusFinished,
		Value:  "done",
		Usage:  agent.Usage{CachedTokens: 42},
		Steps:  []agent.Step{{Index: 1}},
	}); err != nil {
		t.Fatalf("HandleResult() error = %v", err)
	}

	if got := stdout.String(); !strings.Contains(got, "step 1 done [exit_status, exit=1]") {
		t.Fatalf("stdout = %q, want step completion line", got)
	}
	if got := stdout.String(); !strings.Contains(got, "step 1 output> matched file.txt") {
		t.Fatalf("stdout = %q, want step output line", got)
	}
	if got := stdout.String(); !strings.Contains(got, "assistant> done") {
		t.Fatalf("stdout = %q, want assistant result", got)
	}
	if got := stdout.String(); !strings.Contains(got, "usage> cached prompt tokens: 42") {
		t.Fatalf("stdout = %q, want cached token usage", got)
	}
	if got := stderr.String(); !strings.Contains(got, "step 1 error> command failed") {
		t.Fatalf("stderr = %q, want step error", got)
	}
}

func TestShouldUseTUI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		prompt    string
		stdinTTY  bool
		stdoutTTY bool
		want      bool
	}{
		{
			name:      "interactive terminal with no prompt uses tui",
			prompt:    "",
			stdinTTY:  true,
			stdoutTTY: true,
			want:      true,
		},
		{
			name:      "single prompt bypasses tui",
			prompt:    "list files",
			stdinTTY:  true,
			stdoutTTY: true,
			want:      false,
		},
		{
			name:      "non terminal stdin bypasses tui",
			prompt:    "",
			stdinTTY:  false,
			stdoutTTY: true,
			want:      false,
		},
		{
			name:      "non terminal stdout bypasses tui",
			prompt:    "",
			stdinTTY:  true,
			stdoutTTY: false,
			want:      false,
		},
		{
			name:      "blank prompt still allows tui",
			prompt:    "   ",
			stdinTTY:  true,
			stdoutTTY: true,
			want:      true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := shouldUseTUI(tc.prompt, tc.stdinTTY, tc.stdoutTTY); got != tc.want {
				t.Fatalf("shouldUseTUI(%q, %t, %t) = %t, want %t", tc.prompt, tc.stdinTTY, tc.stdoutTTY, got, tc.want)
			}
		})
	}
}

func TestTUIQueueSubmitPromptAndNext(t *testing.T) {
	t.Parallel()

	queue := newTUIQueue()
	trigger, ok := queue.SubmitPrompt("inspect workspace")
	if !ok {
		t.Fatalf("SubmitPrompt() ok = false, want true")
	}

	got, err := queue.Next(context.Background())
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if got.ID != trigger.ID {
		t.Fatalf("trigger.ID = %q, want %q", got.ID, trigger.ID)
	}
	if got.Kind != "repl.prompt" {
		t.Fatalf("trigger.Kind = %q, want repl.prompt", got.Kind)
	}
	if got.Payload != "inspect workspace" {
		t.Fatalf("trigger.Payload = %#v, want inspect workspace", got.Payload)
	}
}

func TestAgentTUIModelHandlesSubmissionAndResultFlow(t *testing.T) {
	t.Parallel()

	events := make(chan tea.Msg)
	queue := newTUIQueue()
	model := newAgentTUIModel(agentTUIOptions{
		events:       events,
		submitPrompt: queue.SubmitPrompt,
		cancel:       func() {},
		modelName:    "gpt-test",
		rootDir:      "/tmp/workspace",
		verbose:      true,
	})

	model.input.SetValue("inspect the directory")
	if cmd := model.handleSubmit(); cmd != nil {
		t.Fatalf("handleSubmit() returned unexpected command")
	}
	if !model.running {
		t.Fatalf("model.running = false, want true")
	}

	trigger, err := queue.Next(context.Background())
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if trigger.Payload != "inspect the directory" {
		t.Fatalf("trigger.Payload = %#v, want inspect the directory", trigger.Payload)
	}

	model.handleDecision(tuiDecisionMsg{
		step:    1,
		thought: "list the directory contents",
		shell:   "ls -la",
	})
	model.handleLiveOutput(tuiLiveOutputMsg{
		stream: transcriptKindStdout,
		text:   "file.txt\n",
	})
	model.handleStep(agent.Step{
		Index:    1,
		Status:   agent.StepStatusOK,
		CWDAfter: "/tmp/workspace",
	})
	model.handleResult(agent.Result{
		Status: agent.ResultStatusFinished,
		Value:  "done",
		CWD:    "/tmp/workspace",
		Usage:  agent.Usage{CachedTokens: 7},
	})

	if model.running {
		t.Fatalf("model.running = true, want false")
	}
	if model.cachedTokens != 7 {
		t.Fatalf("model.cachedTokens = %d, want 7", model.cachedTokens)
	}
	if model.currentCWD != "/tmp/workspace" {
		t.Fatalf("model.currentCWD = %q, want /tmp/workspace", model.currentCWD)
	}

	got := flattenTranscriptEntries(model.entries)
	for _, want := range []string{
		"prompt-1",
		"inspect the directory",
		"step 1 thinking",
		"list the directory contents",
		"step 1 running",
		"ls -la",
		"stdout",
		"file.txt",
		"assistant",
		"done",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("transcript = %q, want %q", got, want)
		}
	}
}

func TestAgentTUIModelShowsToolDecisionAndOutput(t *testing.T) {
	t.Parallel()

	model := newAgentTUIModel(agentTUIOptions{
		events:       make(chan tea.Msg),
		submitPrompt: func(prompt string) (agent.Trigger, bool) { return makeTrigger(prompt, 1), true },
		cancel:       func() {},
		modelName:    "gpt-test",
		rootDir:      "/tmp/workspace",
		verbose:      true,
	})

	model.handleDecision(tuiDecisionMsg{
		step:    2,
		thought: "inspect the config file",
		tool:    agent.ToolNameReadFile,
		input:   `{"file_path":"/tmp/config.yaml"}`,
	})
	model.handleStep(agent.Step{
		Index:                 2,
		Status:                agent.StepStatusOK,
		ActionName:            agent.ToolNameReadFile,
		ActionOutput:          "1|name: demo\n2|enabled: true\n",
		ActionOutputTruncated: true,
		CWDAfter:              "/tmp/workspace",
	})

	got := flattenTranscriptEntries(model.entries)
	for _, want := range []string{
		"step 2 thinking",
		"inspect the config file",
		"step 2 tool",
		"read_file",
		"step 2 input",
		`{"file_path":"/tmp/config.yaml"}`,
		"step 2 output",
		"1|name: demo",
		"[output truncated]",
		"step 2 done",
		"[ok]",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("transcript = %q, want %q", got, want)
		}
	}
}

func TestAgentTUIModelShowsStepErrorsWhenNotVerbose(t *testing.T) {
	t.Parallel()

	model := newAgentTUIModel(agentTUIOptions{
		events:       make(chan tea.Msg),
		submitPrompt: func(prompt string) (agent.Trigger, bool) { return makeTrigger(prompt, 1), true },
		cancel:       func() {},
		modelName:    "gpt-test",
		rootDir:      "/tmp/workspace",
		verbose:      false,
	})

	model.handleResult(agent.Result{
		Status: agent.ResultStatusDriverError,
		Error:  "driver exploded",
		Steps: []agent.Step{{
			Index:      2,
			Status:     agent.StepStatusExitStatus,
			ExitStatus: 1,
			Error:      "command failed",
		}},
	})

	got := flattenTranscriptEntries(model.entries)
	for _, want := range []string{
		"step 2",
		"exit_status (exit=1)",
		"command failed",
		"result",
		"driver_error",
		"driver exploded",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("transcript = %q, want %q", got, want)
		}
	}
	if model.status != "driver_error" {
		t.Fatalf("model.status = %q, want driver_error", model.status)
	}
}

func TestAgentTUIInputShowsLongPromptTail(t *testing.T) {
	t.Parallel()

	model := newAgentTUIModel(agentTUIOptions{
		events:       make(chan tea.Msg),
		submitPrompt: func(prompt string) (agent.Trigger, bool) { return makeTrigger(prompt, 1), true },
		cancel:       func() {},
		modelName:    "gpt-test",
		rootDir:      "/tmp/workspace",
		verbose:      true,
	})
	model.width = 48
	model.height = 18
	model.resize()
	model.input.SetValue("tell me which one and I can help too")

	view := model.View()
	for _, want := range []string{
		"tell me which one",
		"help too",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("view = %q, want %q", view, want)
		}
	}
}

func TestAgentTUIInputShowsPromptOnlyOnceWhenEmpty(t *testing.T) {
	t.Parallel()

	model := newAgentTUIModel(agentTUIOptions{
		events:       make(chan tea.Msg),
		submitPrompt: func(prompt string) (agent.Trigger, bool) { return makeTrigger(prompt, 1), true },
		cancel:       func() {},
		modelName:    "gpt-test",
		rootDir:      "/tmp/workspace",
		verbose:      true,
	})
	model.width = 64
	model.height = 18
	model.resize()
	model.input.SetValue("")

	view := model.View()
	if got := strings.Count(view, "agent>"); got != 1 {
		t.Fatalf("strings.Count(view, %q) = %d, want 1", "agent>", got)
	}
}

func TestAgentTUITranscriptWrapsLongAssistantText(t *testing.T) {
	t.Parallel()

	model := newAgentTUIModel(agentTUIOptions{
		events:       make(chan tea.Msg),
		submitPrompt: func(prompt string) (agent.Trigger, bool) { return makeTrigger(prompt, 1), true },
		cancel:       func() {},
		modelName:    "gpt-test",
		rootDir:      "/tmp/workspace",
		verbose:      true,
	})
	model.width = 52
	model.height = 24
	model.resize()

	rendered := model.renderEntry(transcriptEntry{
		kind:  transcriptKindAssistant,
		title: "assistant",
		body:  "On quiet mornings, when the light slips through the window in soft bands and the world has not yet fully decided what kind of day it wants to be.",
	})
	if got := strings.Count(rendered, "\n"); got < 2 {
		t.Fatalf("rendered entry = %q, want wrapped body with multiple lines", rendered)
	}
	for _, want := range []string{
		"what kind of day it wants to",
		"be.",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered entry = %q, want %q", rendered, want)
		}
	}
}

func TestAgentTUIModelMouseWheelScrollsTranscriptWhileInputFocused(t *testing.T) {
	t.Parallel()

	model := newAgentTUIModel(agentTUIOptions{
		events:       make(chan tea.Msg),
		submitPrompt: func(prompt string) (agent.Trigger, bool) { return makeTrigger(prompt, 1), true },
		cancel:       func() {},
		modelName:    "gpt-test",
		rootDir:      "/tmp/workspace",
		verbose:      true,
	})
	model.width = 64
	model.height = 18
	model.resize()

	for i := 0; i < 24; i++ {
		model.appendEntry(transcriptEntry{
			kind:  transcriptKindAssistant,
			title: "assistant",
			body:  strings.Repeat("line ", 6),
		})
	}
	if model.focus != agentTUIFocusInput {
		t.Fatalf("model.focus = %v, want input focus", model.focus)
	}
	if model.transcript.YOffset == 0 {
		t.Fatalf("transcript.YOffset = 0, want scrollable content")
	}

	before := model.transcript.YOffset
	next, _ := model.Update(tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonWheelUp,
	})
	updated, ok := next.(agentTUIModel)
	if !ok {
		t.Fatalf("updated model type = %T, want agentTUIModel", next)
	}
	if updated.focus != agentTUIFocusInput {
		t.Fatalf("updated.focus = %v, want input focus", updated.focus)
	}
	if updated.transcript.YOffset >= before {
		t.Fatalf("transcript.YOffset = %d, want less than %d after wheel up", updated.transcript.YOffset, before)
	}
}

func flattenTranscriptEntries(entries []transcriptEntry) string {
	var b strings.Builder
	for _, entry := range entries {
		b.WriteString(entry.title)
		b.WriteString("\n")
		b.WriteString(entry.body)
		b.WriteString("\n")
	}
	return b.String()
}
