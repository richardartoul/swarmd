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
		agent:   a,
		stepNum: stepIndex,
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

	stdoutCapture := newCaptureWriter(a.maxOutputBytes, a.liveStdout)
	stderrCapture := newCaptureWriter(a.maxOutputBytes, a.liveStderr)
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

	err = a.runner.Run(runCtx, prog)
	restoreDir = false
	step.CWDAfter = a.runner.Dir
	step.Stdout, step.StdoutTruncated = stdoutCapture.Snapshot()
	step.Stderr, step.StderrTruncated = stderrCapture.Snapshot()

	classified, runErr := classifyRunResult(*step, err)
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
	step.ActionOutput, step.ActionOutputTruncated = truncateText(output, toolscommon.MaxInt(1, limit))
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
