package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/richardartoul/swarmd/pkg/agent"
	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
)

const (
	testStructuredOutputToolName = "test_structured_output"
	structuredOutputNeedle       = "structured-payload-segment"
)

var registerStructuredOutputToolOnce sync.Once

func TestHandleTriggerRetainsLargeToolOutputUntilAgentClose(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(root+"/note.txt", []byte(strings.Repeat("line\n", 256)), 0o644); err != nil {
		t.Fatalf("os.WriteFile(note.txt) error = %v", err)
	}

	var (
		spillPath string
		spillData string
	)
	a := newAgent(t, agent.Config{
		Root:           root,
		MaxOutputBytes: 96,
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				tool(agent.ToolNameReadFile, agent.ToolKindFunction, `{"file_path":"note.txt","offset":1,"limit":256}`),
				finish("done"),
			},
		},
		OnStep: agent.StepHandlerFunc(func(_ context.Context, _ agent.Trigger, step agent.Step) error {
			if len(step.ActionOutputFiles) != 1 {
				t.Fatalf("len(step.ActionOutputFiles) = %d, want 1", len(step.ActionOutputFiles))
			}
			spillPath = step.ActionOutputFiles[0].Path
			data, err := os.ReadFile(spillPath)
			if err != nil {
				t.Fatalf("os.ReadFile(spillPath) error = %v", err)
			}
			spillData = string(data)
			return nil
		}),
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "spill-tool-output"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	step := result.Steps[0]
	if !step.ActionOutputTruncated {
		t.Fatal("step.ActionOutputTruncated = false, want true")
	}
	if len(step.ActionOutputFiles) != 1 {
		t.Fatalf("len(step.ActionOutputFiles) = %d, want 1", len(step.ActionOutputFiles))
	}
	if step.ActionOutputBytes <= int64(len(step.ActionOutput)) {
		t.Fatalf("step.ActionOutputBytes = %d, want more than preview length %d", step.ActionOutputBytes, len(step.ActionOutput))
	}
	if !strings.Contains(spillData, "256|line") {
		t.Fatalf("spillData = %q, want full numbered read_file contents", spillData)
	}
	if _, err := os.Stat(spillPath); err != nil {
		t.Fatalf("os.Stat(spillPath) error = %v, want retained spill file before Agent.Close()", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("a.Close() error = %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("second a.Close() error = %v", err)
	}
	if _, err := os.Stat(spillPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("os.Stat(spillPath) error = %v, want %v after Agent.Close()", err, os.ErrNotExist)
	}
}

func TestSweepStaleOutputSpillDirsRemovesStaleDirectories(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	staleDir := root + "/.tmp/tool-outputs/stale-run/step_1"
	if err := os.MkdirAll(staleDir, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(staleDir) error = %v", err)
	}
	if err := os.WriteFile(staleDir+"/leftover.txt", []byte("leftover"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(leftover.txt) error = %v", err)
	}

	fsys, err := sandbox.NewFS(root)
	if err != nil {
		t.Fatalf("sandbox.NewFS() error = %v", err)
	}
	if err := agent.SweepStaleOutputSpillDirs(fsys); err != nil {
		t.Fatalf("SweepStaleOutputSpillDirs() error = %v", err)
	}

	if _, err := os.Stat(root + "/.tmp/tool-outputs"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("os.Stat(tool-outputs) error = %v, want %v after startup sweep", err, os.ErrNotExist)
	}
}

func TestHandleTriggerRetainsShellStdoutUntilAgentClose(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(root+"/big.txt", []byte(strings.Repeat("a", 512)), 0o644); err != nil {
		t.Fatalf("os.WriteFile(big.txt) error = %v", err)
	}

	var (
		stdoutPath string
		stdoutData string
	)
	a := newAgent(t, agent.Config{
		Root:           root,
		MaxOutputBytes: 64,
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				shell("cat big.txt"),
				finish("done"),
			},
		},
		OnStep: agent.StepHandlerFunc(func(_ context.Context, _ agent.Trigger, step agent.Step) error {
			if step.StdoutFile == nil {
				t.Fatal("step.StdoutFile = nil, want spill file reference")
			}
			stdoutPath = step.StdoutFile.Path
			data, err := os.ReadFile(stdoutPath)
			if err != nil {
				t.Fatalf("os.ReadFile(stdoutPath) error = %v", err)
			}
			stdoutData = string(data)
			return nil
		}),
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "spill-shell-output"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	step := result.Steps[0]
	if !step.StdoutTruncated {
		t.Fatal("step.StdoutTruncated = false, want true")
	}
	if step.StdoutFile == nil {
		t.Fatal("step.StdoutFile = nil, want spill file reference")
	}
	if step.StdoutBytes != 512 {
		t.Fatalf("step.StdoutBytes = %d, want 512", step.StdoutBytes)
	}
	if len(step.Stdout) != 64 {
		t.Fatalf("len(step.Stdout) = %d, want 64", len(step.Stdout))
	}
	if len(stdoutData) != 512 {
		t.Fatalf("len(stdoutData) = %d, want 512", len(stdoutData))
	}
	if _, err := os.Stat(stdoutPath); err != nil {
		t.Fatalf("os.Stat(stdoutPath) error = %v, want retained spill file before Agent.Close()", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("a.Close() error = %v", err)
	}
	if _, err := os.Stat(stdoutPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("os.Stat(stdoutPath) error = %v, want %v after Agent.Close()", err, os.ErrNotExist)
	}
}

func TestHandleTriggerRetainsStructuredJSONOutputUntilAgentClose(t *testing.T) {
	t.Parallel()
	ensureStructuredOutputTestTool()

	root := t.TempDir()
	var (
		fullOutputPath string
		fullOutput     string
	)
	a := newAgent(t, agent.Config{
		Root:           root,
		MaxOutputBytes: 4096,
		ConfiguredTools: []agent.ConfiguredTool{{
			ID: testStructuredOutputToolName,
		}},
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				tool(testStructuredOutputToolName, agent.ToolKindFunction, `{"items":48}`),
				finish("done"),
			},
		},
		OnStep: agent.StepHandlerFunc(func(_ context.Context, _ agent.Trigger, step agent.Step) error {
			for _, file := range step.ActionOutputFiles {
				if strings.HasSuffix(file.Path, "/full_output.json") {
					fullOutputPath = file.Path
					break
				}
			}
			if fullOutputPath == "" {
				t.Fatal("full_output.json file reference was not recorded on the step")
			}
			data, err := os.ReadFile(fullOutputPath)
			if err != nil {
				t.Fatalf("os.ReadFile(fullOutputPath) error = %v", err)
			}
			fullOutput = string(data)
			return nil
		}),
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "spill-structured-output"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	step := result.Steps[0]
	if !step.ActionOutputCompacted {
		t.Fatal("step.ActionOutputCompacted = false, want true")
	}
	if !step.ActionOutputTruncated {
		t.Fatal("step.ActionOutputTruncated = false, want true")
	}
	if !json.Valid([]byte(step.ActionOutput)) {
		t.Fatalf("step.ActionOutput = %q, want valid JSON stub", step.ActionOutput)
	}
	if !strings.Contains(step.ActionOutput, `"tool":"`+testStructuredOutputToolName+`"`) {
		t.Fatalf("step.ActionOutput = %q, want preserved tool name", step.ActionOutput)
	}
	if !strings.Contains(step.ActionOutput, `"kind":"json_stub_v1"`) {
		t.Fatalf("step.ActionOutput = %q, want stub metadata", step.ActionOutput)
	}
	if !strings.Contains(step.ActionOutput, `"files"`) {
		t.Fatalf("step.ActionOutput = %q, want files field", step.ActionOutput)
	}
	if !strings.Contains(fullOutput, structuredOutputNeedle) {
		t.Fatalf("fullOutput = %q, want original large payload", fullOutput)
	}
	if _, err := os.Stat(fullOutputPath); err != nil {
		t.Fatalf("os.Stat(fullOutputPath) error = %v, want retained spill file before Agent.Close()", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("a.Close() error = %v", err)
	}
	if _, err := os.Stat(fullOutputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("os.Stat(fullOutputPath) error = %v, want %v after Agent.Close()", err, os.ErrNotExist)
	}
}

func TestHandleTriggerStructuredOutputFallsBackWhenStubCannotFitPreviewBudget(t *testing.T) {
	t.Parallel()
	ensureStructuredOutputTestTool()

	root := t.TempDir()
	a := newAgent(t, agent.Config{
		Root:           root,
		MaxOutputBytes: 64,
		ConfiguredTools: []agent.ConfiguredTool{{
			ID: testStructuredOutputToolName,
		}},
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				tool(testStructuredOutputToolName, agent.ToolKindFunction, `{"items":48}`),
				finish("done"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "spill-structured-budget"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	step := result.Steps[0]
	if len(step.ActionOutput) > 64 {
		t.Fatalf("len(step.ActionOutput) = %d, want <= 64", len(step.ActionOutput))
	}
	if !step.ActionOutputTruncated {
		t.Fatal("step.ActionOutputTruncated = false, want true")
	}
	if step.ActionOutputCompacted {
		t.Fatal("step.ActionOutputCompacted = true, want file-backed fallback notice")
	}
	if !strings.Contains(step.ActionOutput, "Structured JSON output was spilled") {
		t.Fatalf("step.ActionOutput = %q, want fallback spill notice", step.ActionOutput)
	}
	var fullOutputFound bool
	for _, file := range step.ActionOutputFiles {
		if strings.HasSuffix(file.Path, "/full_output.json") {
			fullOutputFound = true
			break
		}
	}
	if !fullOutputFound {
		t.Fatalf("step.ActionOutputFiles = %#v, want full_output.json reference", step.ActionOutputFiles)
	}
}

func TestHandleTriggerMalformedStructuredOutputFallsBackToPlainTextSpill(t *testing.T) {
	t.Parallel()
	ensureStructuredOutputTestTool()

	root := t.TempDir()
	a := newAgent(t, agent.Config{
		Root:           root,
		MaxOutputBytes: 96,
		ConfiguredTools: []agent.ConfiguredTool{{
			ID: testStructuredOutputToolName,
		}},
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				tool(testStructuredOutputToolName, agent.ToolKindFunction, `{"mode":"malformed"}`),
				finish("done"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "spill-malformed-structured"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	step := result.Steps[0]
	if step.ActionOutputCompacted {
		t.Fatal("step.ActionOutputCompacted = true, want plain-text fallback")
	}
	if !step.ActionOutputTruncated {
		t.Fatal("step.ActionOutputTruncated = false, want true")
	}
	if len(step.ActionOutputFiles) != 1 || !strings.HasSuffix(step.ActionOutputFiles[0].Path, "/action_output.txt") {
		t.Fatalf("step.ActionOutputFiles = %#v, want plain-text spill file", step.ActionOutputFiles)
	}
	if !strings.Contains(step.ActionOutput, `"tool":"`+testStructuredOutputToolName+`"`) {
		t.Fatalf("step.ActionOutput = %q, want plain-text preview of malformed payload", step.ActionOutput)
	}
}

func TestHandleTriggerFailsWhenToolSpillWriteFails(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(root+"/note.txt", []byte(strings.Repeat("line\n", 256)), 0o644); err != nil {
		t.Fatalf("os.WriteFile(note.txt) error = %v", err)
	}
	fsys, err := sandbox.NewFS(root)
	if err != nil {
		t.Fatalf("sandbox.NewFS() error = %v", err)
	}
	a := newAgent(t, agent.Config{
		FileSystem:     failingSpillFS{FileSystem: fsys},
		MaxOutputBytes: 96,
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				tool(agent.ToolNameReadFile, agent.ToolKindFunction, `{"file_path":"note.txt","offset":1,"limit":256}`),
				finish("done"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "spill-write-failure"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v, want nil fatal result", err)
	}
	if result.Status != agent.ResultStatusFatalError {
		t.Fatalf("result.Status = %q, want %q", result.Status, agent.ResultStatusFatalError)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("len(result.Steps) = %d, want 1", len(result.Steps))
	}
	if result.Steps[0].Status != agent.StepStatusFatalError {
		t.Fatalf("step.Status = %q, want %q", result.Steps[0].Status, agent.StepStatusFatalError)
	}
	if !strings.Contains(result.Steps[0].Error, "spill write failed") {
		t.Fatalf("step.Error = %q, want spill write failure", result.Steps[0].Error)
	}
}

func TestSessionCarriesCompactedStructuredOutputWithRetainedSpillFiles(t *testing.T) {
	t.Parallel()
	ensureStructuredOutputTestTool()

	driver := &recordingDriver{
		decisions: []agent.Decision{
			tool(testStructuredOutputToolName, agent.ToolKindFunction, `{"items":48}`),
			finish("first"),
			finish("second"),
		},
	}
	a := newAgent(t, agent.Config{
		Root:           t.TempDir(),
		MaxOutputBytes: 4096,
		ConfiguredTools: []agent.ConfiguredTool{{
			ID: testStructuredOutputToolName,
		}},
		Driver: driver,
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
	if first.Status != agent.ResultStatusFinished {
		t.Fatalf("first.Status = %q, want %q", first.Status, agent.ResultStatusFinished)
	}
	second, err := session.RunTrigger(context.Background(), agent.Trigger{
		ID:      "turn-2",
		Kind:    "repl.prompt",
		Payload: "second question",
	})
	if err != nil {
		t.Fatalf("second RunTrigger() error = %v", err)
	}
	if second.Status != agent.ResultStatusFinished {
		t.Fatalf("second.Status = %q, want %q", second.Status, agent.ResultStatusFinished)
	}
	if len(driver.requests) < 3 {
		t.Fatalf("len(driver.requests) = %d, want at least 3", len(driver.requests))
	}
	if len(driver.requests[2].Steps) != 1 {
		t.Fatalf("len(second turn prior steps) = %d, want 1", len(driver.requests[2].Steps))
	}
	prior := driver.requests[2].Steps[0]
	if !prior.ActionOutputCompacted {
		t.Fatal("prior.ActionOutputCompacted = false, want true")
	}
	if strings.Contains(prior.ActionOutput, "structured-payload-segment-048") {
		t.Fatalf("prior.ActionOutput = %q, want compacted stub instead of the full payload set", prior.ActionOutput)
	}
	if strings.Count(prior.ActionOutput, structuredOutputNeedle) > 3 {
		t.Fatalf("prior.ActionOutput = %q, want bounded preview entries only", prior.ActionOutput)
	}
	if !strings.Contains(prior.ActionOutput, `"kind":"json_stub_v1"`) {
		t.Fatalf("prior.ActionOutput = %q, want stored JSON stub", prior.ActionOutput)
	}
	var fullOutputPath string
	for _, file := range prior.ActionOutputFiles {
		if strings.HasSuffix(file.Path, "/full_output.json") {
			fullOutputPath = file.Path
			break
		}
	}
	if fullOutputPath == "" {
		t.Fatal("prior step did not retain a full_output.json file reference")
	}
	if _, err := os.Stat(fullOutputPath); err != nil {
		t.Fatalf("os.Stat(fullOutputPath) error = %v, want retained spill file during later session turn", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("a.Close() error = %v", err)
	}
	if _, err := os.Stat(fullOutputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("os.Stat(fullOutputPath) error = %v, want %v after Agent.Close()", err, os.ErrNotExist)
	}
}

func TestHandleTriggerRetainsDistinctRunSpillDirsUntilAgentClose(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(root+"/note.txt", []byte(strings.Repeat("line\n", 256)), 0o644); err != nil {
		t.Fatalf("os.WriteFile(note.txt) error = %v", err)
	}

	a := newAgent(t, agent.Config{
		Root:           root,
		MaxOutputBytes: 96,
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				tool(agent.ToolNameReadFile, agent.ToolKindFunction, `{"file_path":"note.txt","offset":1,"limit":256}`),
				finish("first"),
				tool(agent.ToolNameReadFile, agent.ToolKindFunction, `{"file_path":"note.txt","offset":1,"limit":256}`),
				finish("second"),
			},
		},
	})

	first, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "spill-run-one"})
	if err != nil {
		t.Fatalf("first HandleTrigger() error = %v", err)
	}
	second, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "spill-run-two"})
	if err != nil {
		t.Fatalf("second HandleTrigger() error = %v", err)
	}
	if len(first.Steps) != 1 || len(first.Steps[0].ActionOutputFiles) != 1 {
		t.Fatalf("first step files = %#v, want one spill file", first.Steps)
	}
	if len(second.Steps) != 1 || len(second.Steps[0].ActionOutputFiles) != 1 {
		t.Fatalf("second step files = %#v, want one spill file", second.Steps)
	}
	firstPath := first.Steps[0].ActionOutputFiles[0].Path
	secondPath := second.Steps[0].ActionOutputFiles[0].Path
	if firstPath == secondPath {
		t.Fatalf("firstPath = %q, secondPath = %q, want distinct retained spill paths", firstPath, secondPath)
	}
	if _, err := os.Stat(firstPath); err != nil {
		t.Fatalf("os.Stat(firstPath) error = %v, want retained spill file", err)
	}
	if _, err := os.Stat(secondPath); err != nil {
		t.Fatalf("os.Stat(secondPath) error = %v, want retained spill file", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("a.Close() error = %v", err)
	}
	if _, err := os.Stat(firstPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("os.Stat(firstPath) error = %v, want %v after Agent.Close()", err, os.ErrNotExist)
	}
	if _, err := os.Stat(secondPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("os.Stat(secondPath) error = %v, want %v after Agent.Close()", err, os.ErrNotExist)
	}
}

func ensureStructuredOutputTestTool() {
	registerStructuredOutputToolOnce.Do(func() {
		agent.RegisterTool(testStructuredOutputToolPlugin{})
	})
}

type testStructuredOutputToolPlugin struct{}

func (testStructuredOutputToolPlugin) Definition() toolscore.ToolDefinition {
	return toolscore.ToolDefinition{
		Name:        testStructuredOutputToolName,
		Description: "Emit a large structured JSON payload for runtime spill tests.",
		Kind:        toolscore.ToolKindFunction,
		Parameters: toolscommon.ObjectSchema(map[string]any{
			"items": toolscommon.IntegerSchema("How many items to emit."),
		}),
	}
}

func (testStructuredOutputToolPlugin) NewHandler(toolscore.ConfiguredTool) (toolscore.ToolHandler, error) {
	return toolscore.ToolHandlerFunc(func(_ context.Context, toolCtx toolscore.ToolContext, step *toolscore.Step, call *toolscore.ToolAction) error {
		type input struct {
			Items int    `json:"items"`
			Mode  string `json:"mode"`
		}
		request, err := toolscore.DecodeToolInput[input](call.Input)
		if err != nil {
			toolCtx.SetParseError(step, err)
			return nil
		}
		switch strings.TrimSpace(request.Mode) {
		case "malformed":
			toolCtx.SetOutput(step, `{"tool":"`+testStructuredOutputToolName+`","action":"emit","ok":true} trailing-data `+strings.Repeat("x", 256))
			return nil
		}
		if request.Items <= 0 {
			request.Items = 48
		}
		items := make([]map[string]any, 0, request.Items)
		for idx := 1; idx <= request.Items; idx++ {
			items = append(items, map[string]any{
				"id":      idx,
				"name":    fmt.Sprintf("job-%d", idx),
				"status":  "completed",
				"details": fmt.Sprintf("%s-%03d-%s", structuredOutputNeedle, idx, strings.Repeat("x", 96)),
			})
		}
		output, err := toolscommon.MarshalToolOutput(map[string]any{
			"tool":     testStructuredOutputToolName,
			"action":   "emit",
			"ok":       true,
			"warnings": []string{},
			"page_info": map[string]any{
				"page":          1,
				"per_page":      request.Items,
				"has_next_page": false,
			},
			"files": []toolscore.FileReference{{
				Path:        "artifacts/report.txt",
				MimeType:    "text/plain",
				Description: "Generated report",
				SizeBytes:   512,
			}},
			"data": map[string]any{
				"items":       items,
				"items_count": len(items),
			},
		})
		if err != nil {
			return err
		}
		toolCtx.SetOutput(step, output)
		return nil
	}), nil
}

type failingSpillFS struct {
	sandbox.FileSystem
}

func (f failingSpillFS) WriteFile(name string, data []byte, perm os.FileMode) error {
	if strings.Contains(name, string(os.PathSeparator)+".tmp"+string(os.PathSeparator)+"tool-outputs"+string(os.PathSeparator)) {
		return fmt.Errorf("spill write failed")
	}
	return f.FileSystem.WriteFile(name, data, perm)
}
