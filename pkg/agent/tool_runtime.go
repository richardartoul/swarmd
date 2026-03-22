// See LICENSE for licensing information

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
)

func (a *Agent) runToolStep(ctx context.Context, trigger Trigger, stepIndex int, decision Decision) (Step, error) {
	step := Step{
		Index:          stepIndex,
		Type:           StepTypeTool,
		Thought:        decision.Thought,
		ActionName:     decision.Tool.Name,
		ActionToolKind: decision.Tool.Kind,
		ActionInput:    decision.Tool.Input,
		Usage:          decision.Usage,
		CWDBefore:      a.runner.Dir,
		CWDAfter:       a.runner.Dir,
		StartedAt:      time.Now(),
	}
	defer func() {
		step.FinishedAt = time.Now()
		step.Duration = step.FinishedAt.Sub(step.StartedAt)
		if step.CWDAfter == "" && a.runner != nil {
			step.CWDAfter = a.runner.Dir
		}
	}()

	runCtx := ctx
	cancel := func() {}
	if a.stepTimeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, a.stepTimeout)
	}
	defer cancel()
	runCtx = contextWithTrigger(runCtx, trigger)

	handler, ok := a.toolHandlerByName[decision.Tool.Name]
	if !ok {
		step.Status = StepStatusPolicyError
		step.Error = fmt.Sprintf("tool %q is not implemented", decision.Tool.Name)
		return step, nil
	}
	toolCtx := runtimeToolContext{
		agent:    a,
		stepNum:  stepIndex,
		toolName: decision.Tool.Name,
	}
	err := handler.Invoke(runCtx, toolCtx, &step, decision.Tool)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			step.Status = StepStatusFatalError
			step.Error = err.Error()
			return step, err
		}
		step.Status = StepStatusFatalError
		step.Error = err.Error()
		return step, err
	}
	if step.Status == StepStatusFatalError {
		if strings.TrimSpace(step.Error) == "" {
			step.Error = "tool step failed"
		}
		return step, errors.New(step.Error)
	}
	if step.Status == "" {
		step.Status = StepStatusOK
	}
	return step, nil
}

func (a *Agent) runShellTool(ctx context.Context, stepNum int, step *Step, exec toolscore.ShellExecution) error {
	command := strings.TrimSpace(exec.Command)
	if command == "" {
		setToolPolicyError(step, fmt.Errorf("command must not be empty"))
		return nil
	}
	step.Shell = command

	originalDir := a.runner.Dir
	if workdir := strings.TrimSpace(exec.Workdir); workdir != "" {
		resolved, err := a.resolveToolPath(workdir)
		if err != nil {
			setToolPolicyError(step, err)
			return nil
		}
		info, err := a.fileSystem.Stat(resolved)
		if err != nil {
			setToolPolicyError(step, err)
			return nil
		}
		if !info.IsDir() {
			setToolPolicyError(step, fmt.Errorf("%q is not a directory", resolved))
			return nil
		}
		a.runner.Dir = resolved
		step.CWDAfter = resolved
	}

	restoreDir := true
	defer func() {
		if restoreDir {
			a.runner.Dir = originalDir
			step.CWDAfter = originalDir
		}
	}()

	prog, err := a.parser.Parse(strings.NewReader(command), fmt.Sprintf("agent-tool-step-%d", stepNum))
	if err != nil {
		setToolParseError(step, err)
		return nil
	}
	if err := validateProgram(prog); err != nil {
		setToolPolicyError(step, err)
		return nil
	}

	stdoutPath, stdoutRef, err := a.stepSpillFile(stepNum, "stdout.txt", "text/plain", "Full stdout from run_shell")
	if err != nil {
		return err
	}
	stderrPath, stderrRef, err := a.stepSpillFile(stepNum, "stderr.txt", "text/plain", "Full stderr from run_shell")
	if err != nil {
		return err
	}
	stdoutCapture := newCaptureWriter(a.maxOutputBytes, a.outputFileThreshold(), &captureSpillConfig{
		FileSystem:  a.fileSystem,
		Path:        stdoutPath,
		MimeType:    stdoutRef.MimeType,
		Description: stdoutRef.Description,
	}, a.liveStdout)
	stderrCapture := newCaptureWriter(a.maxOutputBytes, a.outputFileThreshold(), &captureSpillConfig{
		FileSystem:  a.fileSystem,
		Path:        stderrPath,
		MimeType:    stderrRef.MimeType,
		Description: stderrRef.Description,
	}, a.liveStderr)
	a.stdout.Set(stdoutCapture)
	a.stderr.Set(stderrCapture)
	defer func() {
		a.stdout.Set(io.Discard)
		a.stderr.Set(io.Discard)
	}()

	runCtx := ctx
	cancel := func() {}
	if timeout := toolscommon.BoundedDurationMillis(exec.TimeoutMS, 0, a.stepTimeout); timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	runErr := a.runner.Run(runCtx, prog)
	restoreDir = false
	step.CWDAfter = a.runner.Dir
	stdoutSnapshot, err := stdoutCapture.Snapshot()
	if err != nil {
		return err
	}
	stderrSnapshot, err := stderrCapture.Snapshot()
	if err != nil {
		return err
	}
	step.Stdout = stdoutSnapshot.Preview
	step.StdoutTruncated = stdoutSnapshot.Truncated
	step.StdoutBytes = stdoutSnapshot.TotalBytes
	step.StdoutFile = stdoutSnapshot.File
	step.Stderr = stderrSnapshot.Preview
	step.StderrTruncated = stderrSnapshot.Truncated
	step.StderrBytes = stderrSnapshot.TotalBytes
	step.StderrFile = stderrSnapshot.File

	classified, runErr := classifyRunResult(*step, runErr)
	*step = classified
	return runErr
}

func (a *Agent) resolveToolPath(path string) (string, error) {
	return sandbox.ResolvePath(a.fileSystem, a.runner.Dir, path)
}

func (a *Agent) setToolOutput(step *Step, output string) {
	limit := a.maxOutputBytes
	if limit <= 0 {
		limit = DefaultMaxOutputBytes
	}
	step.ActionOutputBytes = int64(len(output))
	step.ActionOutputFiles = nil
	step.ActionOutputCompacted = false

	applied, err := a.applyStructuredToolOutput(step, output)
	if err != nil {
		step.ActionOutput, step.ActionOutputTruncated = truncateText(output, toolscommon.MaxInt(1, limit))
		step.Status = StepStatusFatalError
		step.Error = err.Error()
		return
	}
	if applied {
		return
	}

	threshold := a.outputFileThreshold()
	if threshold > 0 && len(output) > threshold {
		ref, err := a.writeStepSpillFile(step.Index, "action_output.txt", "text/plain", "Full tool output", []byte(output))
		if err != nil {
			step.ActionOutput, step.ActionOutputTruncated = truncateText(output, toolscommon.MaxInt(1, limit))
			step.Status = StepStatusFatalError
			step.Error = err.Error()
			return
		}
		step.ActionOutputFiles = []toolscore.FileReference{*ref}
	}
	step.ActionOutput, step.ActionOutputTruncated = truncateText(output, toolscommon.MaxInt(1, limit))
}

func (a *Agent) applyStructuredToolOutput(step *Step, output string) (bool, error) {
	limit := a.maxOutputBytes
	if limit <= 0 {
		limit = DefaultMaxOutputBytes
	}
	if len(output) <= limit {
		return false, nil
	}
	budget := toolscommon.MaxInt(1, toolscommon.MinInt(limit, toolscommon.DefaultJSONStubBytes))
	probe, ok, err := toolscommon.CompactStructuredJSONOutput(output, toolscommon.JSONCompactOptions{
		Budget: budget,
	})
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	fullOutputRef, err := a.writeStepSpillFile(step.Index, "full_output.json", "application/json", "Full structured tool output", []byte(output))
	if err != nil {
		return false, err
	}
	compacted, _, err := toolscommon.CompactStructuredJSONOutput(output, toolscommon.JSONCompactOptions{
		Budget:         budget,
		FullOutputFile: fullOutputRef,
	})
	if err != nil {
		return false, err
	}
	if len(compacted.Output) > limit {
		step.ActionOutput = fallbackStructuredOutputPreview(*fullOutputRef, step.ActionOutputBytes, limit)
		step.ActionOutputTruncated = true
		step.ActionOutputFiles = toolscommon.NormalizeFileReferences(append(probe.Files, *fullOutputRef))
		step.ActionOutputCompacted = false
		return true, nil
	}
	step.ActionOutput = compacted.Output
	step.ActionOutputTruncated = compacted.Compacted
	step.ActionOutputFiles = compacted.Files
	step.ActionOutputCompacted = compacted.Compacted
	if len(step.ActionOutputFiles) == 0 {
		step.ActionOutputFiles = probe.Files
	}
	return true, nil
}

func fallbackStructuredOutputPreview(file toolscore.FileReference, originalBytes int64, limit int) string {
	message := fmt.Sprintf(
		"Structured JSON output was spilled to %s (%d bytes). Read the file for the full payload.",
		strings.TrimSpace(file.Path),
		originalBytes,
	)
	preview, _ := truncateText(message, toolscommon.MaxInt(1, limit))
	return preview
}

func truncateText(text string, limit int) (string, bool) {
	if limit <= 0 || len(text) <= limit {
		return text, false
	}
	return text[:limit], true
}

func setToolPolicyError(step *Step, err error) {
	step.Status = StepStatusPolicyError
	step.Error = err.Error()
}

func setToolParseError(step *Step, err error) {
	step.Status = StepStatusParseError
	step.Error = err.Error()
}

func mustCompactJSON(value any) string {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "{}"
	}
	return strings.TrimSpace(b.String())
}
