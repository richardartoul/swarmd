package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/richardartoul/swarmd/pkg/agent"
)

func TestStripConfiguredTools(t *testing.T) {
	tools := []agent.ConfiguredTool{{ID: "slack_post"}, {ID: "datadog_read"}, {ID: "server_log"}}
	got := stripConfiguredTools(tools, []string{"slack_post", "slack_dm"})
	if len(got) != 2 || got[0].ID != "datadog_read" || got[1].ID != "server_log" {
		t.Fatalf("stripConfiguredTools() = %#v", got)
	}
}

func TestJSONStepsEmitterWritesStepAndResult(t *testing.T) {
	var buf bytes.Buffer
	emitter := newJSONStepsEmitter(&buf)
	if err := emitter.HandleStep(context.Background(), agent.Trigger{}, agent.Step{
		Index:      1,
		ActionName: "server_log",
		Status:     agent.StepStatusOK,
	}); err != nil {
		t.Fatal(err)
	}
	if err := emitter.HandleResult(context.Background(), agent.Result{
		Status: agent.ResultStatusFinished,
		Value:  "done",
	}); err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	var step map[string]any
	if err := json.Unmarshal(lines[0], &step); err != nil {
		t.Fatal(err)
	}
	if step["type"] != "step" || step["action_name"] != "server_log" {
		t.Fatalf("step record = %#v", step)
	}
	var result map[string]any
	if err := json.Unmarshal(lines[1], &result); err != nil {
		t.Fatal(err)
	}
	if result["type"] != "result" || result["status"] != string(agent.ResultStatusFinished) {
		t.Fatalf("result record = %#v", result)
	}
}
