package agent

import (
	"strings"
	"testing"
)

func TestBuildStepReplayFunctionTool(t *testing.T) {
	t.Parallel()

	replay, ok := BuildStepReplay(Step{
		Index:          1,
		Thought:        "inspect the file",
		ActionName:     ToolNameReadFile,
		ActionToolKind: ToolKindFunction,
		ActionInput:    `{"file_path":"/tmp/demo.txt"}`,
		Status:         StepStatusOK,
		CWDAfter:       "/workspace",
		ActionOutput:   "File contents here.",
	})
	if !ok {
		t.Fatal("BuildStepReplay() ok = false, want true")
	}
	if replay.CallID != "step_1" {
		t.Fatalf("replay.CallID = %q, want %q", replay.CallID, "step_1")
	}
	if replay.ToolName != ToolNameReadFile {
		t.Fatalf("replay.ToolName = %q, want %q", replay.ToolName, ToolNameReadFile)
	}
	if replay.ToolKind != ToolKindFunction {
		t.Fatalf("replay.ToolKind = %q, want %q", replay.ToolKind, ToolKindFunction)
	}
	if replay.Input != `{"file_path":"/tmp/demo.txt"}` {
		t.Fatalf("replay.Input = %q, want function input", replay.Input)
	}
	if replay.Thought != "inspect the file" {
		t.Fatalf("replay.Thought = %q, want thought", replay.Thought)
	}
	if !strings.Contains(replay.Output, "Observation for step 1") {
		t.Fatalf("replay.Output = %q, want observation heading", replay.Output)
	}
}

func TestBuildStepReplayCustomTool(t *testing.T) {
	t.Parallel()

	replay, ok := BuildStepReplay(Step{
		Index:          2,
		Thought:        "apply the patch",
		ActionName:     ToolNameApplyPatch,
		ActionToolKind: ToolKindCustom,
		ActionInput:    "*** Begin Patch\n*** End Patch",
		Status:         StepStatusOK,
		CWDAfter:       "/workspace",
	})
	if !ok {
		t.Fatal("BuildStepReplay() ok = false, want true")
	}
	if replay.ToolKind != ToolKindCustom {
		t.Fatalf("replay.ToolKind = %q, want %q", replay.ToolKind, ToolKindCustom)
	}
	if replay.Input != "*** Begin Patch\n*** End Patch" {
		t.Fatalf("replay.Input = %q, want raw custom input", replay.Input)
	}
}

func TestBuildStepReplayRunShellNormalizesCommandInput(t *testing.T) {
	t.Parallel()

	replay, ok := BuildStepReplay(Step{
		Index:      3,
		Type:       StepTypeShell,
		Thought:    "check the current directory",
		ActionName: ToolNameRunShell,
		Shell:      "pwd",
		Status:     StepStatusOK,
		CWDAfter:   "/workspace",
		Stdout:     "/workspace\n",
	})
	if !ok {
		t.Fatal("BuildStepReplay() ok = false, want true")
	}
	if replay.CallID != "step_3" {
		t.Fatalf("replay.CallID = %q, want %q", replay.CallID, "step_3")
	}
	if replay.ToolName != ToolNameRunShell {
		t.Fatalf("replay.ToolName = %q, want %q", replay.ToolName, ToolNameRunShell)
	}
	if replay.ToolKind != ToolKindFunction {
		t.Fatalf("replay.ToolKind = %q, want %q", replay.ToolKind, ToolKindFunction)
	}
	if replay.Input != `{"command":"pwd"}` {
		t.Fatalf("replay.Input = %q, want normalized run_shell input", replay.Input)
	}
	if !strings.Contains(replay.Output, "Stdout:\n/workspace\n") {
		t.Fatalf("replay.Output = %q, want stdout summary", replay.Output)
	}
}

func TestBuildStepReplaySkipsNonActionSteps(t *testing.T) {
	t.Parallel()

	if _, ok := BuildStepReplay(Step{Index: 4, Status: StepStatusParseError, Error: "bad parse"}); ok {
		t.Fatal("BuildStepReplay() ok = true, want false for step without action name")
	}
}

func TestBuildStepReplayIncludesObservationSummaryAndTruncationMarkers(t *testing.T) {
	t.Parallel()

	replay, ok := BuildStepReplay(Step{
		Index:                 5,
		ActionName:            ToolNameReadFile,
		ActionToolKind:        ToolKindFunction,
		ActionInput:           `{"file_path":"/tmp/demo.txt"}`,
		Status:                StepStatusExitStatus,
		ExitStatus:            1,
		Error:                 "tool failed",
		CWDAfter:              "/workspace",
		ActionOutput:          "partial output",
		ActionOutputTruncated: true,
		Stdout:                "stdout text",
		StdoutTruncated:       true,
		Stderr:                "stderr text",
		StderrTruncated:       true,
	})
	if !ok {
		t.Fatal("BuildStepReplay() ok = false, want true")
	}
	if !strings.Contains(replay.Output, "Exit status: 1") {
		t.Fatalf("replay.Output = %q, want exit status", replay.Output)
	}
	if !strings.Contains(replay.Output, "Output was truncated.") {
		t.Fatalf("replay.Output = %q, want output truncation marker", replay.Output)
	}
	if !strings.Contains(replay.Output, "Stdout was truncated.") {
		t.Fatalf("replay.Output = %q, want stdout truncation marker", replay.Output)
	}
	if !strings.Contains(replay.Output, "Stderr was truncated.") {
		t.Fatalf("replay.Output = %q, want stderr truncation marker", replay.Output)
	}
}
