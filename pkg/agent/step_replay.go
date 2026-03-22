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
	IsError  bool
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
		IsError:  stepReplayIsError(step),
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
		if step.ActionOutputCompacted {
			b.WriteString("Output was compacted.\n")
		} else if len(step.ActionOutputFiles) > 0 {
			b.WriteString("Output was truncated. Full output is available in files.\n")
		} else {
			b.WriteString("Output was truncated.\n")
		}
	}
	if !step.ActionOutputCompacted {
		appendFileReferenceList(&b, "Output files", step.ActionOutputFiles)
	}
	if step.Stdout != "" {
		fmt.Fprintf(&b, "Stdout:\n%s\n", step.Stdout)
	}
	if step.StdoutTruncated {
		if step.StdoutFile != nil {
			b.WriteString("Stdout was truncated. Full stdout is available in a file.\n")
		} else {
			b.WriteString("Stdout was truncated.\n")
		}
	}
	if step.StdoutFile != nil {
		appendFileReference(&b, "Stdout file", *step.StdoutFile)
	}
	if step.Stderr != "" {
		fmt.Fprintf(&b, "Stderr:\n%s\n", step.Stderr)
	}
	if step.StderrTruncated {
		if step.StderrFile != nil {
			b.WriteString("Stderr was truncated. Full stderr is available in a file.\n")
		} else {
			b.WriteString("Stderr was truncated.\n")
		}
	}
	if step.StderrFile != nil {
		appendFileReference(&b, "Stderr file", *step.StderrFile)
	}
	return b.String()
}

func appendFileReferenceList(b *strings.Builder, label string, files []FileReference) {
	if len(files) == 0 {
		return
	}
	fmt.Fprintf(b, "%s:\n", label)
	for _, file := range files {
		appendFileReferenceLine(b, file)
	}
}

func appendFileReference(b *strings.Builder, label string, file FileReference) {
	fmt.Fprintf(b, "%s:\n", label)
	appendFileReferenceLine(b, file)
}

func appendFileReferenceLine(b *strings.Builder, file FileReference) {
	if strings.TrimSpace(file.Path) == "" {
		return
	}
	b.WriteString("- ")
	b.WriteString(strings.TrimSpace(file.Path))
	if file.SizeBytes > 0 {
		fmt.Fprintf(b, " (%d bytes", file.SizeBytes)
		if strings.TrimSpace(file.MimeType) != "" {
			fmt.Fprintf(b, ", %s", strings.TrimSpace(file.MimeType))
		}
		b.WriteString(")")
	} else if strings.TrimSpace(file.MimeType) != "" {
		fmt.Fprintf(b, " (%s)", strings.TrimSpace(file.MimeType))
	}
	if strings.TrimSpace(file.Description) != "" {
		fmt.Fprintf(b, " - %s", strings.TrimSpace(file.Description))
	}
	b.WriteString("\n")
}

func stepReplayIsError(step Step) bool {
	switch step.Status {
	case StepStatusParseError, StepStatusPolicyError, StepStatusFatalError:
		return true
	}
	return step.ExitStatus != 0
}
