package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/richardartoul/swarmd/pkg/agent"
	"github.com/richardartoul/swarmd/pkg/sh/interp"
	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
)

func TestHandleTriggerKeepsStateWithinTrigger(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	resolvedRoot := resolveRoot(t, root)
	a := newAgent(t, agent.Config{
		Root: root,
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				shell(`foo=hello; mkdir work; cd work; printf '%s' "$foo" > note.txt`),
				shell(`cat note.txt; printf '\n'; pwd`),
				finish("done"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "trigger-1"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	if result.Status != agent.ResultStatusFinished {
		t.Fatalf("result.Status = %q, want %q", result.Status, agent.ResultStatusFinished)
	}
	if len(result.Steps) != 2 {
		t.Fatalf("len(result.Steps) = %d, want 2", len(result.Steps))
	}
	if result.Steps[0].Status != agent.StepStatusOK {
		t.Fatalf("step 1 status = %q, want %q", result.Steps[0].Status, agent.StepStatusOK)
	}
	if result.Steps[1].Status != agent.StepStatusOK {
		t.Fatalf("step 2 status = %q, want %q", result.Steps[1].Status, agent.StepStatusOK)
	}

	want := "hello\n" + filepath.Join(resolvedRoot, "work")
	got := strings.TrimSpace(result.Steps[1].Stdout)
	if got != want {
		t.Fatalf("step 2 stdout = %q, want %q", got, want)
	}
}

func TestHandleTriggerRecordsFinishThought(t *testing.T) {
	t.Parallel()

	a := newAgent(t, agent.Config{
		Root: t.TempDir(),
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				finishWithThought("done", "all requested work is complete"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "trigger-finish-thought"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	if got := result.FinishThought; got != "all requested work is complete" {
		t.Fatalf("result.FinishThought = %q, want %q", got, "all requested work is complete")
	}
	if got := result.Value; got != "done" {
		t.Fatalf("result.Value = %#v, want %q", got, "done")
	}
}

func TestHandleTriggerResetsBetweenTriggersByDefault(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	resolvedRoot := resolveRoot(t, root)
	a := newAgent(t, agent.Config{
		Root: root,
		Driver: agent.DriverFunc(func(ctx context.Context, req agent.Request) (agent.Decision, error) {
			switch req.Trigger.ID {
			case "one":
				if req.Step == 1 {
					return shell("mkdir sub; cd sub; pwd"), nil
				}
				if req.Step == 2 {
					return finish(nil), nil
				}
			case "two":
				if req.Step == 1 {
					return shell("pwd"), nil
				}
				if req.Step == 2 {
					return finish(nil), nil
				}
			}
			return agent.Decision{}, fmt.Errorf("unexpected request: trigger=%q step=%d", req.Trigger.ID, req.Step)
		}),
	})

	first, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "one"})
	if err != nil {
		t.Fatalf("first HandleTrigger() error = %v", err)
	}
	second, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "two"})
	if err != nil {
		t.Fatalf("second HandleTrigger() error = %v", err)
	}

	if strings.TrimSpace(first.Steps[0].Stdout) != filepath.Join(resolvedRoot, "sub") {
		t.Fatalf("first pwd = %q, want %q", strings.TrimSpace(first.Steps[0].Stdout), filepath.Join(resolvedRoot, "sub"))
	}
	if strings.TrimSpace(second.Steps[0].Stdout) != resolvedRoot {
		t.Fatalf("second pwd = %q, want %q", strings.TrimSpace(second.Steps[0].Stdout), resolvedRoot)
	}
}

func TestHandleTriggerPreservesStateBetweenTriggersWhenConfigured(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	resolvedRoot := resolveRoot(t, root)
	a := newAgent(t, agent.Config{
		Root:                         root,
		PreserveStateBetweenTriggers: true,
		Driver: agent.DriverFunc(func(ctx context.Context, req agent.Request) (agent.Decision, error) {
			switch req.Trigger.ID {
			case "one":
				if req.Step == 1 {
					return shell("mkdir sub; cd sub; pwd"), nil
				}
				if req.Step == 2 {
					return finish(nil), nil
				}
			case "two":
				if req.Step == 1 {
					return shell("pwd"), nil
				}
				if req.Step == 2 {
					return finish(nil), nil
				}
			}
			return agent.Decision{}, fmt.Errorf("unexpected request: trigger=%q step=%d", req.Trigger.ID, req.Step)
		}),
	})

	if _, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "one"}); err != nil {
		t.Fatalf("first HandleTrigger() error = %v", err)
	}
	second, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "two"})
	if err != nil {
		t.Fatalf("second HandleTrigger() error = %v", err)
	}

	if strings.TrimSpace(second.Steps[0].Stdout) != filepath.Join(resolvedRoot, "sub") {
		t.Fatalf("second pwd = %q, want %q", strings.TrimSpace(second.Steps[0].Stdout), filepath.Join(resolvedRoot, "sub"))
	}
}

func TestSessionReplaysConversationAndStepsAcrossTurns(t *testing.T) {
	t.Parallel()

	driver := &recordingDriver{
		decisions: []agent.Decision{
			shell("printf 'first'"),
			finish("first answer"),
			shell("printf 'second'"),
			finish("second answer"),
		},
	}
	a := newAgent(t, agent.Config{
		Root:     t.TempDir(),
		MaxSteps: 2,
		Driver:   driver,
	})
	session := agent.NewSession(a)

	first, err := session.RunTrigger(context.Background(), agent.Trigger{
		ID:      "turn-1",
		Kind:    "repl.prompt",
		Payload: "first question",
	})
	if err != nil {
		t.Fatalf("first RunTrigger() error = %v", err)
	}
	second, err := session.RunTrigger(context.Background(), agent.Trigger{
		ID:      "turn-2",
		Kind:    "repl.prompt",
		Payload: "second question",
	})
	if err != nil {
		t.Fatalf("second RunTrigger() error = %v", err)
	}

	if first.Status != agent.ResultStatusFinished {
		t.Fatalf("first.Status = %q, want %q", first.Status, agent.ResultStatusFinished)
	}
	if second.Status != agent.ResultStatusFinished {
		t.Fatalf("second.Status = %q, want %q", second.Status, agent.ResultStatusFinished)
	}
	if len(second.Steps) != 1 {
		t.Fatalf("len(second.Steps) = %d, want 1", len(second.Steps))
	}
	if second.Steps[0].Index != 2 {
		t.Fatalf("second step index = %d, want 2", second.Steps[0].Index)
	}
	if got := agent.StepCallID(second.Steps[0]); got != "step_2" {
		t.Fatalf("StepCallID(second step) = %q, want %q", got, "step_2")
	}

	if len(driver.requests) != 4 {
		t.Fatalf("len(driver.requests) = %d, want 4", len(driver.requests))
	}
	secondTurnFirstRequest := driver.requests[2]
	if secondTurnFirstRequest.Step != 1 {
		t.Fatalf("second turn request step = %d, want 1", secondTurnFirstRequest.Step)
	}
	if len(secondTurnFirstRequest.Steps) != 1 {
		t.Fatalf("len(second turn request steps) = %d, want 1", len(secondTurnFirstRequest.Steps))
	}
	if secondTurnFirstRequest.Steps[0].Index != 1 {
		t.Fatalf("prior replayed step index = %d, want 1", secondTurnFirstRequest.Steps[0].Index)
	}
	if got := agent.StepCallID(secondTurnFirstRequest.Steps[0]); got != "step_1" {
		t.Fatalf("StepCallID(prior step) = %q, want %q", got, "step_1")
	}

	var (
		userMessages      []string
		assistantMessages []string
	)
	for _, message := range secondTurnFirstRequest.Messages {
		switch message.Role {
		case agent.MessageRoleUser:
			userMessages = append(userMessages, message.Content)
		case agent.MessageRoleAssistant:
			assistantMessages = append(assistantMessages, message.Content)
		}
		if strings.Contains(message.Content, "Trigger context\n") {
			t.Fatalf("session message = %q, want plain session turns without trigger wrapper", message.Content)
		}
	}
	if !slices.Contains(userMessages, "first question") {
		t.Fatalf("user messages = %#v, want prior user turn", userMessages)
	}
	if !slices.Contains(userMessages, "second question") {
		t.Fatalf("user messages = %#v, want current user turn", userMessages)
	}
	if !slices.Contains(assistantMessages, "first answer") {
		t.Fatalf("assistant messages = %#v, want prior assistant reply", assistantMessages)
	}
}

func TestSessionCarriesFailedTurnStepsWithoutAssistantReply(t *testing.T) {
	t.Parallel()

	driver := &recordingDriver{
		decisions: []agent.Decision{
			shell("printf 'partial'"),
			finish("second answer"),
		},
	}
	a := newAgent(t, agent.Config{
		Root:     t.TempDir(),
		MaxSteps: 1,
		Driver:   driver,
	})
	session := agent.NewSession(a)

	first, err := session.RunTrigger(context.Background(), agent.Trigger{
		ID:      "turn-1",
		Kind:    "repl.prompt",
		Payload: "first question",
	})
	if err != nil {
		t.Fatalf("first RunTrigger() error = %v", err)
	}
	second, err := session.RunTrigger(context.Background(), agent.Trigger{
		ID:      "turn-2",
		Kind:    "repl.prompt",
		Payload: "second question",
	})
	if err != nil {
		t.Fatalf("second RunTrigger() error = %v", err)
	}

	if first.Status != agent.ResultStatusMaxSteps {
		t.Fatalf("first.Status = %q, want %q", first.Status, agent.ResultStatusMaxSteps)
	}
	if second.Status != agent.ResultStatusFinished {
		t.Fatalf("second.Status = %q, want %q", second.Status, agent.ResultStatusFinished)
	}

	if len(driver.requests) != 2 {
		t.Fatalf("len(driver.requests) = %d, want 2", len(driver.requests))
	}
	secondTurnRequest := driver.requests[1]
	if secondTurnRequest.Step != 1 {
		t.Fatalf("second turn request step = %d, want 1", secondTurnRequest.Step)
	}
	if len(secondTurnRequest.Steps) != 1 {
		t.Fatalf("len(second turn request steps) = %d, want 1", len(secondTurnRequest.Steps))
	}
	if secondTurnRequest.Steps[0].Index != 1 {
		t.Fatalf("replayed failed step index = %d, want 1", secondTurnRequest.Steps[0].Index)
	}

	var assistantMessages []string
	for _, message := range secondTurnRequest.Messages {
		if message.Role == agent.MessageRoleAssistant {
			assistantMessages = append(assistantMessages, message.Content)
		}
	}
	if len(assistantMessages) != 0 {
		t.Fatalf("assistant messages = %#v, want no assistant reply for prior max-step turn", assistantMessages)
	}
}

func TestHandleTriggerCarriesToolReplayDataToNextDriverRequest(t *testing.T) {
	t.Parallel()

	replayData := `[{"type":"thinking","thinking":"","signature":"sig-step-1"}]`
	driver := &recordingDriver{
		decisions: []agent.Decision{
			{
				Tool: &agent.ToolAction{
					Name:  agent.ToolNameRunShell,
					Kind:  agent.ToolKindFunction,
					Input: `{"command":"printf step-one"}`,
				},
				ReplayData: replayData,
			},
			finish("done"),
		},
	}
	a := newAgent(t, agent.Config{
		Root:   t.TempDir(),
		Driver: driver,
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "replay-data"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	if result.Status != agent.ResultStatusFinished {
		t.Fatalf("result.Status = %q, want %q", result.Status, agent.ResultStatusFinished)
	}
	if len(driver.requests) != 2 {
		t.Fatalf("len(driver.requests) = %d, want 2", len(driver.requests))
	}
	if got := driver.requests[1].StepReplayData["step_1"]; got != replayData {
		t.Fatalf("second request StepReplayData[step_1] = %q, want %q", got, replayData)
	}
}

func TestHandleTriggerIncludesSandboxRootInDriverRequest(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	resolvedRoot := resolveRoot(t, root)
	var seen agent.Request
	a := newAgent(t, agent.Config{
		Root: root,
		Driver: agent.DriverFunc(func(ctx context.Context, req agent.Request) (agent.Decision, error) {
			seen = req
			return finish("done"), nil
		}),
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "root-guidance"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	if result.Status != agent.ResultStatusFinished {
		t.Fatalf("result.Status = %q, want %q", result.Status, agent.ResultStatusFinished)
	}
	if got := seen.SandboxRoot; got != resolvedRoot {
		t.Fatalf("request.SandboxRoot = %q, want %q", got, resolvedRoot)
	}
	if got := seen.CWD; got != resolvedRoot {
		t.Fatalf("request.CWD = %q, want %q", got, resolvedRoot)
	}
	if len(seen.Messages) == 0 {
		t.Fatal("request.Messages = empty, want current-state message")
	}
	last := seen.Messages[len(seen.Messages)-1].Content
	if !strings.Contains(last, "Sandbox root: "+resolvedRoot) {
		t.Fatalf("current-state message = %q, want sandbox root line", last)
	}
	if !strings.Contains(last, "Paths outside the sandbox root are inaccessible.") {
		t.Fatalf("current-state message = %q, want sandbox boundary guidance", last)
	}
}

func TestHandleTriggerUsesInjectedFileSystem(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := osWriteFile(filepath.Join(root, "mounted.txt"), []byte("injected\n")); err != nil {
		t.Fatal(err)
	}
	fsys := &recordingSandboxFS{root: root}
	a := newAgent(t, agent.Config{
		FileSystem: fsys,
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				shell("cat mounted.txt"),
				finish(nil),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "injected-fs"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	if got := result.Steps[0].Stdout; got != "injected\n" {
		t.Fatalf("step stdout = %q, want %q", got, "injected\n")
	}
	if fsys.openCount == 0 {
		t.Fatal("injected filesystem Open() was never called")
	}
}

func TestHandleTriggerTurnsParseErrorsIntoObservations(t *testing.T) {
	t.Parallel()

	a := newAgent(t, agent.Config{
		Root: t.TempDir(),
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				shell("if then"),
				finish(nil),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "parse"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	if result.Status != agent.ResultStatusFinished {
		t.Fatalf("result.Status = %q, want %q", result.Status, agent.ResultStatusFinished)
	}
	if got := result.Steps[0].Status; got != agent.StepStatusParseError {
		t.Fatalf("step status = %q, want %q", got, agent.StepStatusParseError)
	}
}

func TestHandleTriggerTurnsExitStatusesIntoObservations(t *testing.T) {
	t.Parallel()

	a := newAgent(t, agent.Config{
		Root: t.TempDir(),
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				shell("false"),
				finish(nil),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "false"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	step := result.Steps[0]
	if step.Status != agent.StepStatusExitStatus {
		t.Fatalf("step status = %q, want %q", step.Status, agent.StepStatusExitStatus)
	}
	if step.ExitStatus != 1 {
		t.Fatalf("step exit status = %d, want 1", step.ExitStatus)
	}
	if result.Status != agent.ResultStatusFinished {
		t.Fatalf("result status = %q, want %q", result.Status, agent.ResultStatusFinished)
	}
}

func TestHandleTriggerRejectsDisallowedShellConstructs(t *testing.T) {
	t.Parallel()

	a := newAgent(t, agent.Config{
		Root: t.TempDir(),
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				shell("echo hi &"),
				shell("exec true"),
				shell("exit 0"),
				shell(`sh -c 'echo hi &'`),
				finish(nil),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "policy"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	if result.Status != agent.ResultStatusFinished {
		t.Fatalf("result status = %q, want %q", result.Status, agent.ResultStatusFinished)
	}
	for i, step := range result.Steps {
		if step.Status != agent.StepStatusPolicyError {
			t.Fatalf("step %d status = %q, want %q", i+1, step.Status, agent.StepStatusPolicyError)
		}
	}
}

func TestHandleTriggerRejectsPlainGrepAtRuntime(t *testing.T) {
	t.Parallel()

	a := newAgent(t, agent.Config{
		Root: t.TempDir(),
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				shell(`printf 'hello\n' | grep hello`),
				finish(nil),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "plain-grep-policy"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("len(result.Steps) = %d, want 1", len(result.Steps))
	}
	if result.Steps[0].Status != agent.StepStatusExitStatus {
		t.Fatalf("step status = %q, want %q", result.Steps[0].Status, agent.StepStatusExitStatus)
	}
	if result.Steps[0].ExitStatus != 2 {
		t.Fatalf("step exit status = %d, want 2", result.Steps[0].ExitStatus)
	}
	if !strings.Contains(result.Steps[0].Stderr, "plain grep without -E/-F is not allowed") {
		t.Fatalf("step stderr = %q, want explicit grep mode rejection", result.Steps[0].Stderr)
	}
}

func TestHandleTriggerAllowsExplicitGrepMode(t *testing.T) {
	t.Parallel()

	a := newAgent(t, agent.Config{
		Root: t.TempDir(),
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				shell(`printf 'hello\n' | grep -F hello`),
				finish("done"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "explicit-grep-mode"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("len(result.Steps) = %d, want 1", len(result.Steps))
	}
	if result.Steps[0].Status != agent.StepStatusOK {
		t.Fatalf("step status = %q, want %q", result.Steps[0].Status, agent.StepStatusOK)
	}
	if strings.TrimSpace(result.Steps[0].Stdout) != "hello" {
		t.Fatalf("step stdout = %q, want %q", result.Steps[0].Stdout, "hello")
	}
}

func TestHandleTriggerAllowsDynamicGrepModeAtRuntime(t *testing.T) {
	t.Parallel()

	a := newAgent(t, agent.Config{
		Root: t.TempDir(),
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				shell(`mode=-F; printf 'hello\n' | grep "$mode" hello`),
				finish("done"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "non-literal-grep"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("len(result.Steps) = %d, want 1", len(result.Steps))
	}
	if result.Steps[0].Status != agent.StepStatusOK {
		t.Fatalf("step status = %q, want %q", result.Steps[0].Status, agent.StepStatusOK)
	}
	if strings.TrimSpace(result.Steps[0].Stdout) != "hello" {
		t.Fatalf("step stdout = %q, want %q", result.Steps[0].Stdout, "hello")
	}
}

func TestHandleTriggerBlocksDynamicNestedShAtRuntime(t *testing.T) {
	t.Parallel()

	a := newAgent(t, agent.Config{
		Root: t.TempDir(),
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				shell(`cmd='echo hi &'; sh -c "$cmd"`),
				finish(nil),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "dynamic-nested-sh"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("len(result.Steps) = %d, want 1", len(result.Steps))
	}
	if result.Steps[0].Status != agent.StepStatusExitStatus {
		t.Fatalf("step status = %q, want %q", result.Steps[0].Status, agent.StepStatusExitStatus)
	}
	if !strings.Contains(result.Steps[0].Stderr, "sh: background jobs are not allowed in agent shell steps") {
		t.Fatalf("step stderr = %q, want nested sh rejection", result.Steps[0].Stderr)
	}
}

func TestHandleTriggerBlocksDynamicExitAtRuntime(t *testing.T) {
	t.Parallel()

	a := newAgent(t, agent.Config{
		Root: t.TempDir(),
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				shell("cmd=exit; $cmd 0"),
				finish(nil),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "dynamic-exit"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	step := result.Steps[0]
	if step.Status != agent.StepStatusExitStatus {
		t.Fatalf("step status = %q, want %q", step.Status, agent.StepStatusExitStatus)
	}
	if !strings.Contains(step.Stderr, "exit: disabled in agent shell steps") {
		t.Fatalf("step stderr = %q, want dynamic exit rejection", step.Stderr)
	}
	if result.Status != agent.ResultStatusFinished {
		t.Fatalf("result status = %q, want %q", result.Status, agent.ResultStatusFinished)
	}
}

func TestHandleTriggerBlocksWrappedControlBuiltinsAtRuntime(t *testing.T) {
	t.Parallel()

	a := newAgent(t, agent.Config{
		Root: t.TempDir(),
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				shell("xargs exit 0"),
				shell("xargs exec true"),
				finish(nil),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "wrapped-control-builtins"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	if len(result.Steps) != 2 {
		t.Fatalf("len(result.Steps) = %d, want 2", len(result.Steps))
	}

	if result.Steps[0].Status != agent.StepStatusExitStatus {
		t.Fatalf("step 1 status = %q, want %q", result.Steps[0].Status, agent.StepStatusExitStatus)
	}
	if !strings.Contains(result.Steps[0].Stderr, "exit: disabled in agent shell steps") {
		t.Fatalf("step 1 stderr = %q, want wrapped exit rejection", result.Steps[0].Stderr)
	}

	if result.Steps[1].Status != agent.StepStatusExitStatus {
		t.Fatalf("step 2 status = %q, want %q", result.Steps[1].Status, agent.StepStatusExitStatus)
	}
	if !strings.Contains(result.Steps[1].Stderr, "exec: disabled in agent shell steps") {
		t.Fatalf("step 2 stderr = %q, want wrapped exec rejection", result.Steps[1].Stderr)
	}
}

func TestHandleTriggerBlocksCommandExitAtRuntime(t *testing.T) {
	t.Parallel()

	a := newAgent(t, agent.Config{
		Root: t.TempDir(),
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				shell("command exit 0"),
				finish(nil),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "command-exit"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("len(result.Steps) = %d, want 1", len(result.Steps))
	}
	if result.Steps[0].Status != agent.StepStatusExitStatus {
		t.Fatalf("step status = %q, want %q", result.Steps[0].Status, agent.StepStatusExitStatus)
	}
	if !strings.Contains(result.Steps[0].Stderr, "exit: disabled in agent shell steps") {
		t.Fatalf("step stderr = %q, want command exit rejection", result.Steps[0].Stderr)
	}
}

func TestHandleTriggerEnforcesOutputLimit(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := osWriteFile(filepath.Join(root, "big.txt"), bytes.Repeat([]byte("a"), 512)); err != nil {
		t.Fatal(err)
	}

	a := newAgent(t, agent.Config{
		Root:           root,
		MaxOutputBytes: 64,
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				shell("cat big.txt"),
				finish(nil),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "big-output"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	step := result.Steps[0]
	if !step.StdoutTruncated {
		t.Fatalf("step.StdoutTruncated = false, want true")
	}
	if len(step.Stdout) != 64 {
		t.Fatalf("len(step.Stdout) = %d, want 64", len(step.Stdout))
	}
}

func TestHandleTriggerCallsOnStep(t *testing.T) {
	t.Parallel()

	var got []agent.Step
	a := newAgent(t, agent.Config{
		Root: t.TempDir(),
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				withThought(shell("pwd"), "look around"),
				finish("done"),
			},
		},
		OnStep: agent.StepHandlerFunc(func(ctx context.Context, trigger agent.Trigger, step agent.Step) error {
			got = append(got, step)
			return nil
		}),
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "with-step-hook"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	if result.Status != agent.ResultStatusFinished {
		t.Fatalf("result.Status = %q, want %q", result.Status, agent.ResultStatusFinished)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].Thought != "look around" {
		t.Fatalf("got[0].Thought = %q, want %q", got[0].Thought, "look around")
	}
	if got[0].Shell != "pwd" {
		t.Fatalf("got[0].Shell = %q, want %q", got[0].Shell, "pwd")
	}
}

func TestHandleTriggerBuildsDriverMessages(t *testing.T) {
	t.Parallel()

	driver := &recordingDriver{
		decisions: []agent.Decision{
			finish("done"),
		},
	}
	a := newAgent(t, agent.Config{
		Root:         t.TempDir(),
		Driver:       driver,
		SystemPrompt: "test prompt",
	})

	_, err := a.HandleTrigger(context.Background(), agent.Trigger{
		ID:      "prompt-1",
		Kind:    "repl.prompt",
		Payload: "list files",
		Metadata: map[string]any{
			"source": "test",
		},
	})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	if len(driver.requests) != 1 {
		t.Fatalf("len(driver.requests) = %d, want 1", len(driver.requests))
	}
	req := driver.requests[0]
	if req.Step != 1 {
		t.Fatalf("req.Step = %d, want 1", req.Step)
	}
	if len(req.Messages) != 4 {
		t.Fatalf("len(req.Messages) = %d, want 4", len(req.Messages))
	}
	if req.Messages[0].Role != agent.MessageRoleSystem || req.Messages[0].Content != "test prompt" {
		t.Fatalf("req.Messages[0] = %#v, want system prompt", req.Messages[0])
	}
	if req.Messages[1].Role != agent.MessageRoleSystem || !strings.Contains(req.Messages[1].Content, "Runtime-only run_shell guidance:") {
		t.Fatalf("req.Messages[1] = %#v, want runtime-only run_shell system message", req.Messages[1])
	}
	if !strings.Contains(req.Messages[1].Content, "Use run_shell only as a sandboxed fallback when no structured tool fits.") {
		t.Fatalf("req.Messages[1] = %#v, want run_shell fallback reminder", req.Messages[1])
	}
	if req.Messages[2].Role != agent.MessageRoleUser {
		t.Fatalf("req.Messages[2].Role = %q, want %q", req.Messages[2].Role, agent.MessageRoleUser)
	}
	if !strings.Contains(req.Messages[2].Content, "Trigger context") {
		t.Fatalf("trigger context = %q, want heading", req.Messages[2].Content)
	}
	if !strings.Contains(req.Messages[2].Content, `"source": "test"`) {
		t.Fatalf("trigger context = %q, want rendered metadata", req.Messages[2].Content)
	}
	if !strings.Contains(req.Messages[2].Content, "User prompt:\nlist files") {
		t.Fatalf("trigger context = %q, want rendered prompt", req.Messages[2].Content)
	}
	if req.Messages[3].Role != agent.MessageRoleUser {
		t.Fatalf("req.Messages[3].Role = %q, want %q", req.Messages[3].Role, agent.MessageRoleUser)
	}
	if !strings.Contains(req.Messages[3].Content, "Current step number: 1") {
		t.Fatalf("current state = %q, want step number", req.Messages[3].Content)
	}
	if !strings.Contains(req.Messages[3].Content, "No prior steps have been run for this trigger.") {
		t.Fatalf("current state = %q, want empty-step note", req.Messages[3].Content)
	}
	if strings.Contains(req.Messages[3].Content, "Focused retry guidance for the last failed tool:") {
		t.Fatalf("current state = %q, want no retry guidance on first turn", req.Messages[3].Content)
	}
}

func TestHandleTriggerCarriesPriorStepsWithoutConversationReplayMessages(t *testing.T) {
	t.Parallel()

	driver := &recordingDriver{
		decisions: []agent.Decision{
			shell("pwd"),
			finish("done"),
		},
	}
	a := newAgent(t, agent.Config{
		Root:         t.TempDir(),
		Driver:       driver,
		SystemPrompt: "test prompt",
	})

	_, err := a.HandleTrigger(context.Background(), agent.Trigger{
		ID:      "prompt-1",
		Kind:    "repl.prompt",
		Payload: "where am i",
	})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	if len(driver.requests) != 2 {
		t.Fatalf("len(driver.requests) = %d, want 2", len(driver.requests))
	}
	second := driver.requests[1]
	if len(second.Messages) != 4 {
		t.Fatalf("len(second.Messages) = %d, want 4", len(second.Messages))
	}
	if second.Messages[2].Role != agent.MessageRoleUser || !strings.Contains(second.Messages[2].Content, "Trigger context") {
		t.Fatalf("second.Messages[2] = %#v, want trigger context", second.Messages[2])
	}
	for idx, message := range second.Messages {
		if strings.Contains(message.Content, `"type":"function_call"`) || strings.Contains(message.Content, `"type":"custom_tool_call"`) {
			t.Fatalf("second.Messages[%d].Content = %q, want no serialized prior tool calls", idx, message.Content)
		}
		if strings.Contains(message.Content, `"type":"function_call_output"`) || strings.Contains(message.Content, `"type":"custom_tool_call_output"`) {
			t.Fatalf("second.Messages[%d].Content = %q, want no serialized prior tool outputs", idx, message.Content)
		}
	}
	if len(second.Steps) != 1 {
		t.Fatalf("len(second.Steps) = %d, want 1 prior step", len(second.Steps))
	}
	if second.Steps[0].ActionName != agent.ToolNameRunShell {
		t.Fatalf("second.Steps[0].ActionName = %q, want %q", second.Steps[0].ActionName, agent.ToolNameRunShell)
	}
	if second.Steps[0].Shell != "pwd" {
		t.Fatalf("second.Steps[0].Shell = %q, want %q", second.Steps[0].Shell, "pwd")
	}
	if second.Messages[3].Role != agent.MessageRoleUser {
		t.Fatalf("second.Messages[3].Role = %q, want %q", second.Messages[3].Role, agent.MessageRoleUser)
	}
	if !strings.Contains(second.Messages[3].Content, "Current step number: 2") {
		t.Fatalf("current state = %q, want updated step number", second.Messages[3].Content)
	}
}

func TestHandleTriggerUsesNetworkAwareDefaultSystemPrompt(t *testing.T) {
	t.Parallel()

	driver := &recordingDriver{
		decisions: []agent.Decision{
			finish("done"),
		},
	}
	a := newAgent(t, agent.Config{
		Root:          t.TempDir(),
		Driver:        driver,
		NetworkDialer: interp.OSNetworkDialer{},
	})

	if _, err := a.HandleTrigger(context.Background(), agent.Trigger{
		ID:      "prompt-1",
		Kind:    "repl.prompt",
		Payload: "fetch a URL",
	}); err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	if len(driver.requests) != 1 {
		t.Fatalf("len(driver.requests) = %d, want 1", len(driver.requests))
	}
	if len(driver.requests[0].Messages) == 0 {
		t.Fatal("driver request had no messages")
	}
	var systemPrompts []string
	for _, message := range driver.requests[0].Messages {
		if message.Role == agent.MessageRoleSystem {
			systemPrompts = append(systemPrompts, message.Content)
		}
	}
	combinedPrompts := strings.Join(systemPrompts, "\n")
	if !strings.Contains(combinedPrompts, "Network-capable tools may be available through the interpreter-owned dialer") {
		t.Fatalf("combined system prompts = %q, want network-enabled instructions", combinedPrompts)
	}
	if !strings.Contains(combinedPrompts, "Runtime-only run_shell guidance:") {
		t.Fatalf("combined system prompts = %q, want runtime-only run_shell guidance", combinedPrompts)
	}
	if !strings.Contains(combinedPrompts, "jq") || !strings.Contains(combinedPrompts, "awk") || !strings.Contains(combinedPrompts, "grep") || !strings.Contains(combinedPrompts, "sed") || !strings.Contains(combinedPrompts, "sort") || !strings.Contains(combinedPrompts, "cut") || !strings.Contains(combinedPrompts, "tr") || !strings.Contains(combinedPrompts, "uniq") {
		t.Fatalf("combined system prompts = %q, want updated coreutils guidance", combinedPrompts)
	}
}

func TestHandleTriggerSupportsCustomSandboxCommandsInNestedSh(t *testing.T) {
	t.Parallel()

	a := newAgent(t, agent.Config{
		Root: t.TempDir(),
		CustomCommands: []sandbox.CustomCommand{{
			Info: sandbox.CommandInfo{
				Name:        "trace",
				Usage:       "trace <message...>",
				Description: "write a trace line to stdout",
			},
			Run: func(ctx context.Context, args []string) error {
				hc := interp.HandlerCtx(ctx)
				_, _ = fmt.Fprintln(hc.Stdout, strings.Join(args, " "))
				return nil
			},
		}},
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				shell(`trace outer; sh -c 'trace inner'`),
				finish("done"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "custom-sandbox-command"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	if result.Status != agent.ResultStatusFinished {
		t.Fatalf("result.Status = %q, want %q", result.Status, agent.ResultStatusFinished)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("len(result.Steps) = %d, want 1", len(result.Steps))
	}
	if result.Steps[0].Status != agent.StepStatusOK {
		t.Fatalf("step status = %q, want %q", result.Steps[0].Status, agent.StepStatusOK)
	}
	if got := strings.TrimSpace(result.Steps[0].Stdout); got != "outer\ninner" {
		t.Fatalf("step stdout = %q, want %q", got, "outer\ninner")
	}
}

func TestHandleTriggerCanUseInjectedNetworkDialer(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello")
	}))
	defer server.Close()

	a := newAgent(t, agent.Config{
		Root:          t.TempDir(),
		NetworkDialer: interp.OSNetworkDialer{},
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				shell("curl " + server.URL),
				finish("done"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "curl"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	if result.Status != agent.ResultStatusFinished {
		t.Fatalf("result.Status = %q, want %q", result.Status, agent.ResultStatusFinished)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("len(result.Steps) = %d, want 1", len(result.Steps))
	}
	if result.Steps[0].Status != agent.StepStatusOK {
		t.Fatalf("step status = %q, want %q", result.Steps[0].Status, agent.StepStatusOK)
	}
	if strings.TrimSpace(result.Steps[0].Stdout) != "hello" {
		t.Fatalf("step stdout = %q, want %q", result.Steps[0].Stdout, "hello")
	}
}

func TestHandleTriggerDoesNotReplayConversationAcrossTriggers(t *testing.T) {
	t.Parallel()

	driver := &recordingDriver{
		decisions: []agent.Decision{
			finish("created the file"),
			finish("done"),
		},
	}
	a := newAgent(t, agent.Config{
		Root:         t.TempDir(),
		Driver:       driver,
		SystemPrompt: "test prompt",
	})

	if _, err := a.HandleTrigger(context.Background(), agent.Trigger{
		ID:      "prompt-1",
		Kind:    "repl.prompt",
		Payload: "create a note",
	}); err != nil {
		t.Fatalf("first HandleTrigger() error = %v", err)
	}
	if _, err := a.HandleTrigger(context.Background(), agent.Trigger{
		ID:      "prompt-2",
		Kind:    "repl.prompt",
		Payload: "show it to me",
	}); err != nil {
		t.Fatalf("second HandleTrigger() error = %v", err)
	}

	if len(driver.requests) != 2 {
		t.Fatalf("len(driver.requests) = %d, want 2", len(driver.requests))
	}
	second := driver.requests[1]
	if len(second.Messages) != 4 {
		t.Fatalf("len(second.Messages) = %d, want 4", len(second.Messages))
	}
	if second.Messages[2].Role != agent.MessageRoleUser {
		t.Fatalf("second.Messages[2].Role = %q, want %q", second.Messages[2].Role, agent.MessageRoleUser)
	}
	if strings.Contains(second.Messages[2].Content, "Conversation history across previous triggers") {
		t.Fatalf("trigger context = %q, want no history heading", second.Messages[2].Content)
	}
	if strings.Contains(second.Messages[2].Content, "create a note") {
		t.Fatalf("trigger context = %q, want no prior prompt replay", second.Messages[2].Content)
	}
	if strings.Contains(second.Messages[2].Content, "created the file") {
		t.Fatalf("trigger context = %q, want no prior result replay", second.Messages[2].Content)
	}
	if !strings.Contains(second.Messages[2].Content, "Trigger context") {
		t.Fatalf("second.Messages[2] = %#v, want trigger context", second.Messages[2])
	}
	if !strings.Contains(second.Messages[2].Content, "show it to me") {
		t.Fatalf("second.Messages[2] = %#v, want current prompt only", second.Messages[2])
	}
	if second.Messages[3].Role != agent.MessageRoleUser || !strings.Contains(second.Messages[3].Content, "Current step number: 1") {
		t.Fatalf("second.Messages[3] = %#v, want current state", second.Messages[3])
	}
}

func TestHandleTriggerAccumulatesDriverUsage(t *testing.T) {
	t.Parallel()

	a := newAgent(t, agent.Config{
		Root: t.TempDir(),
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				withCachedTokens(shell("pwd"), 128),
				withCachedTokens(finish("done"), 64),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "usage"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	if result.Usage.CachedTokens != 192 {
		t.Fatalf("result.Usage.CachedTokens = %d, want %d", result.Usage.CachedTokens, 192)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("len(result.Steps) = %d, want 1", len(result.Steps))
	}
	if result.Steps[0].Usage.CachedTokens != 128 {
		t.Fatalf("result.Steps[0].Usage.CachedTokens = %d, want %d", result.Steps[0].Usage.CachedTokens, 128)
	}
}

func TestHandleTriggerEnforcesStepTimeout(t *testing.T) {
	t.Parallel()

	a := newAgent(t, agent.Config{
		Root:        t.TempDir(),
		StepTimeout: 20 * time.Millisecond,
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				shell("while true; do :; done"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "timeout"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v, want nil for per-trigger timeout", err)
	}
	if result.Status != agent.ResultStatusFatalError {
		t.Fatalf("result.Status = %q, want %q", result.Status, agent.ResultStatusFatalError)
	}
	if got := result.Steps[0].Status; got != agent.StepStatusFatalError {
		t.Fatalf("step status = %q, want %q", got, agent.StepStatusFatalError)
	}
	if !strings.Contains(result.Error, context.DeadlineExceeded.Error()) {
		t.Fatalf("result.Error = %q, want timeout", result.Error)
	}
}

func TestServeProcessesQueuedTriggersSequentially(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	triggerCh := make(chan agent.Trigger, 2)
	resultsCh := make(chan agent.Result, 2)

	a := newAgent(t, agent.Config{
		Root:  root,
		Queue: channelQueue{triggers: triggerCh},
		Driver: agent.DriverFunc(func(ctx context.Context, req agent.Request) (agent.Decision, error) {
			if req.Step == 1 {
				return shell(fmt.Sprintf("echo %s", req.Trigger.ID)), nil
			}
			if req.Step == 2 {
				return finish(req.Trigger.ID), nil
			}
			return agent.Decision{}, fmt.Errorf("unexpected step %d", req.Step)
		}),
		OnResult: agent.ResultHandlerFunc(func(ctx context.Context, result agent.Result) error {
			resultsCh <- result
			return nil
		}),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- a.Serve(ctx)
	}()

	triggerCh <- agent.Trigger{ID: "one"}
	triggerCh <- agent.Trigger{ID: "two"}

	first := <-resultsCh
	second := <-resultsCh
	cancel()

	err := <-errCh
	if err == nil || err != context.Canceled {
		t.Fatalf("Serve() error = %v, want %v", err, context.Canceled)
	}
	if first.Trigger.ID != "one" || second.Trigger.ID != "two" {
		t.Fatalf("result order = [%q %q], want [one two]", first.Trigger.ID, second.Trigger.ID)
	}
	if strings.TrimSpace(first.Steps[0].Stdout) != "one" {
		t.Fatalf("first stdout = %q, want %q", strings.TrimSpace(first.Steps[0].Stdout), "one")
	}
	if strings.TrimSpace(second.Steps[0].Stdout) != "two" {
		t.Fatalf("second stdout = %q, want %q", strings.TrimSpace(second.Steps[0].Stdout), "two")
	}
}

type scriptedDriver struct {
	decisions []agent.Decision
	index     int
}

func (d *scriptedDriver) Next(context.Context, agent.Request) (agent.Decision, error) {
	if d.index >= len(d.decisions) {
		return agent.Decision{}, fmt.Errorf("unexpected extra decision request")
	}
	decision := d.decisions[d.index]
	d.index++
	return decision, nil
}

type recordingDriver struct {
	decisions []agent.Decision
	requests  []agent.Request
	index     int
}

func (d *recordingDriver) Next(_ context.Context, req agent.Request) (agent.Decision, error) {
	copied := req
	copied.Messages = append([]agent.Message(nil), req.Messages...)
	copied.Steps = append([]agent.Step(nil), req.Steps...)
	if len(req.StepReplayData) > 0 {
		copied.StepReplayData = make(map[string]string, len(req.StepReplayData))
		for key, value := range req.StepReplayData {
			copied.StepReplayData[key] = value
		}
	}
	d.requests = append(d.requests, copied)
	if d.index >= len(d.decisions) {
		return agent.Decision{}, fmt.Errorf("unexpected extra decision request")
	}
	decision := d.decisions[d.index]
	d.index++
	return decision, nil
}

type channelQueue struct {
	triggers <-chan agent.Trigger
}

type recordingSandboxFS struct {
	interp.OSFileSystem
	root      string
	openCount int
}

func (fsys *recordingSandboxFS) Getwd() (string, error) {
	return fsys.root, nil
}

func (fsys *recordingSandboxFS) Open(name string) (fs.File, error) {
	fsys.openCount++
	return fsys.OSFileSystem.Open(name)
}

func (q channelQueue) Next(ctx context.Context) (agent.Trigger, error) {
	select {
	case <-ctx.Done():
		return agent.Trigger{}, ctx.Err()
	case trigger, ok := <-q.triggers:
		if !ok {
			return agent.Trigger{}, io.EOF
		}
		return trigger, nil
	}
}

func newAgent(t *testing.T, cfg agent.Config) *agent.Agent {
	t.Helper()

	a, err := agent.New(cfg)
	if err != nil {
		t.Fatalf("agent.New() error = %v", err)
	}
	return a
}

func shell(src string) agent.Decision {
	return agent.Decision{
		Tool: &agent.ToolAction{
			Name:  agent.ToolNameRunShell,
			Kind:  agent.ToolKindFunction,
			Input: mustJSON(map[string]any{"command": src}),
		},
	}
}

func finish(value any) agent.Decision {
	return agent.Decision{Finish: &agent.FinishAction{Value: value}}
}

func finishWithThought(value any, thought string) agent.Decision {
	return agent.Decision{
		Thought: thought,
		Finish:  &agent.FinishAction{Value: value},
	}
}

func withCachedTokens(decision agent.Decision, cachedTokens int) agent.Decision {
	decision.Usage.CachedTokens = cachedTokens
	return decision
}

func withThought(decision agent.Decision, thought string) agent.Decision {
	decision.Thought = thought
	return decision
}

func osWriteFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}

func mustJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func resolveRoot(t *testing.T, path string) string {
	t.Helper()

	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("filepath.EvalSymlinks(%q) error = %v", path, err)
	}
	return resolved
}
