package readfile

import (
	"strings"
	"testing"

	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
	"github.com/richardartoul/swarmd/pkg/tools/internal/tooltest"
)

func TestReadFileSliceMode(t *testing.T) {
	t.Parallel()

	toolCtx := tooltest.NewContext(t)
	toolCtx.WriteFile(t, "notes.txt", "alpha\nbeta\ngamma\ndelta\nepsilon\n")

	tests := []struct {
		name         string
		input        string
		wantContains []string
		wantMissing  []string
	}{
		{
			name:         "whole file",
			input:        `{"file_path":"notes.txt"}`,
			wantContains: []string{"Showing lines 1-5 of 5", "1|alpha", "5|epsilon"},
		},
		{
			name:         "offset and limit window",
			input:        `{"file_path":"notes.txt","offset":2,"limit":2}`,
			wantContains: []string{"Showing lines 2-3 of 5", "2|beta", "3|gamma"},
			wantMissing:  []string{"1|alpha", "4|delta"},
		},
		{
			name:         "offset beyond end",
			input:        `{"file_path":"notes.txt","offset":99}`,
			wantContains: []string{"No lines in this range."},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			step := tooltest.Invoke(t, toolCtx, plugin{}, test.input)
			if step.Status != toolscore.StepStatusOK {
				t.Fatalf("step.Status = %q (error %q), want ok", step.Status, step.Error)
			}
			for _, want := range test.wantContains {
				if !strings.Contains(step.ActionOutput, want) {
					t.Fatalf("output = %q, want substring %q", step.ActionOutput, want)
				}
			}
			for _, missing := range test.wantMissing {
				if strings.Contains(step.ActionOutput, missing) {
					t.Fatalf("output = %q, want %q excluded", step.ActionOutput, missing)
				}
			}
		})
	}
}

func TestReadFileIndentationModeSelectsBlock(t *testing.T) {
	t.Parallel()

	toolCtx := tooltest.NewContext(t)
	toolCtx.WriteFile(t, "code.py", strings.Join([]string{
		"def outer():",
		"    def inner():",
		"        return 1",
		"    return inner",
		"",
		"def other():",
		"    return 2",
	}, "\n")+"\n")

	step := tooltest.Invoke(t, toolCtx, plugin{}, `{"file_path":"code.py","mode":"indentation","indentation":{"anchor_line":2}}`)
	if step.Status != toolscore.StepStatusOK {
		t.Fatalf("step.Status = %q (error %q), want ok", step.Status, step.Error)
	}
	if !strings.Contains(step.ActionOutput, "def inner():") || !strings.Contains(step.ActionOutput, "return inner") {
		t.Fatalf("output = %q, want anchored indentation block", step.ActionOutput)
	}
	if strings.Contains(step.ActionOutput, "def other():") {
		t.Fatalf("output = %q, want unrelated top-level block excluded", step.ActionOutput)
	}
}

func TestReadFileRejections(t *testing.T) {
	t.Parallel()

	toolCtx := tooltest.NewContext(t)
	toolCtx.WriteFile(t, "binary.bin", "data\x00data")
	toolCtx.WriteFile(t, "plain.txt", "text\n")
	if err := toolCtx.FS.MkdirAll(toolCtx.Root+"/subdir", 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty path", input: `{"file_path":"  "}`, want: "file_path must not be empty"},
		{name: "missing file", input: `{"file_path":"missing.txt"}`, want: "missing.txt"},
		{name: "directory", input: `{"file_path":"subdir"}`, want: "is a directory"},
		{name: "binary file", input: `{"file_path":"binary.bin"}`, want: "binary file"},
		{name: "escape attempt", input: `{"file_path":"../outside.txt"}`, want: "sandbox"},
		{name: "unknown mode", input: `{"file_path":"plain.txt","mode":"reverse"}`, want: `mode must be "slice" or "indentation"`},
		{name: "indentation config in slice mode", input: `{"file_path":"plain.txt","indentation":{"anchor_line":1}}`, want: `indentation requires mode "indentation"`},
		{name: "unknown argument", input: `{"file_path":"plain.txt","lines":3}`, want: "invalid tool arguments"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			step := tooltest.Invoke(t, toolCtx, plugin{}, test.input)
			if step.Status != toolscore.StepStatusPolicyError {
				t.Fatalf("step.Status = %q (output %q), want policy error", step.Status, step.ActionOutput)
			}
			if !strings.Contains(step.Error, test.want) {
				t.Fatalf("step.Error = %q, want substring %q", step.Error, test.want)
			}
		})
	}
}

func TestReadFileEmptyFile(t *testing.T) {
	t.Parallel()

	toolCtx := tooltest.NewContext(t)
	toolCtx.WriteFile(t, "empty.txt", "")
	step := tooltest.Invoke(t, toolCtx, plugin{}, `{"file_path":"empty.txt"}`)
	if step.Status != toolscore.StepStatusOK {
		t.Fatalf("step.Status = %q (error %q), want ok", step.Status, step.Error)
	}
	if !strings.Contains(step.ActionOutput, "File is empty.") {
		t.Fatalf("output = %q, want empty-file notice", step.ActionOutput)
	}
}
