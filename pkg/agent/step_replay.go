// See LICENSE for licensing information

package agent

import (
	"fmt"
	"strings"
)

// StepReplay is the provider-neutral replay view of a completed action step.
type StepReplay struct {
	CallID   string
	Thought  string
	ToolName string
	ToolKind ToolKind
	Input    string
	Output   string
}

// BuildStepReplay normalizes one completed step into provider-neutral replay data.
func BuildStepReplay(step Step) (StepReplay, bool) {
	name := strings.TrimSpace(step.ActionName)
	if name == "" {
		return StepReplay{}, false
	}

	toolKind := step.ActionToolKind
	if name == ToolNameRunShell && toolKind == "" {
		toolKind = ToolKindFunction
	}

	input := strings.TrimSpace(step.ActionInput)
	if name == ToolNameRunShell && input == "" && strings.TrimSpace(step.Shell) != "" {
		input = mustCompactJSON(map[string]any{"command": strings.TrimSpace(step.Shell)})
	}
	if input == "" && toolKind != ToolKindCustom {
		input = "{}"
	}

	return StepReplay{
		CallID:   StepCallID(step),
		Thought:  strings.TrimSpace(step.Thought),
		ToolName: name,
		ToolKind: toolKind,
		Input:    input,
		Output:   formatStepObservationSummary(step),
	}, true
}

// BuildStepReplays returns replayable prior steps in chronological order.
func BuildStepReplays(steps []Step) []StepReplay {
	if len(steps) == 0 {
		return nil
	}
	replays := make([]StepReplay, 0, len(steps))
	for _, step := range steps {
		replay, ok := BuildStepReplay(step)
		if !ok {
			continue
		}
		replays = append(replays, replay)
	}
	return replays
}

// StepCallID returns the canonical provider-visible call id for a completed step.
func StepCallID(step Step) string {
	return fmt.Sprintf("step_%d", step.Index)
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
