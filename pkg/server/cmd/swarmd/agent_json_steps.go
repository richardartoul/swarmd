package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/richardartoul/swarmd/pkg/agent"
)

type jsonStepsEmitter struct {
	out io.Writer
}

func newJSONStepsEmitter(out io.Writer) *jsonStepsEmitter {
	return &jsonStepsEmitter{out: out}
}

func (e *jsonStepsEmitter) HandleStep(ctx context.Context, trigger agent.Trigger, step agent.Step) error {
	_ = ctx
	_ = trigger
	return e.write(map[string]any{
		"type":         "step",
		"index":        step.Index,
		"action_name":  step.ActionName,
		"status":       step.Status,
		"action_input": step.ActionInput,
		"action_output": truncateJSON(step.ActionOutput, 4096),
		"error":        step.Error,
	})
}

func (e *jsonStepsEmitter) HandleResult(ctx context.Context, result agent.Result) error {
	_ = ctx
	return e.write(map[string]any{
		"type":   "result",
		"status": result.Status,
		"error":  result.Error,
		"value":  agent.RenderResultValue(result.Value),
		"steps":  len(result.Steps),
	})
}

func (e *jsonStepsEmitter) write(v map[string]any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(e.out, "%s\n", data)
	return err
}

func truncateJSON(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
