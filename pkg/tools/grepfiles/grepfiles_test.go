package grepfiles

import (
	"strings"
	"testing"

	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
	"github.com/richardartoul/swarmd/pkg/tools/internal/tooltest"
)

func newGrepContext(t *testing.T) *tooltest.Context {
	t.Helper()
	toolCtx := tooltest.NewContext(t)
	toolCtx.WriteFile(t, "main.go", "package main\n\nfunc main() { println(\"needle\") }\n")
	toolCtx.WriteFile(t, "docs/readme.md", "no match here\n")
	toolCtx.WriteFile(t, "docs/guide.md", "the needle is in this haystack\n")
	toolCtx.WriteFile(t, "binary.bin", "needle\x00binary")
	return toolCtx
}

func TestGrepFilesFindsMatches(t *testing.T) {
	t.Parallel()

	toolCtx := newGrepContext(t)
	step := tooltest.Invoke(t, toolCtx, plugin{}, `{"pattern":"needle"}`)
	if step.Status != toolscore.StepStatusOK {
		t.Fatalf("step.Status = %q (error %q), want ok", step.Status, step.Error)
	}
	for _, want := range []string{"Pattern: needle", "main.go", "docs/guide.md"} {
		if !strings.Contains(step.ActionOutput, want) {
			t.Fatalf("output = %q, want substring %q", step.ActionOutput, want)
		}
	}
	if strings.Contains(step.ActionOutput, "readme.md") {
		t.Fatalf("output = %q, want non-matching file excluded", step.ActionOutput)
	}
	if strings.Contains(step.ActionOutput, "binary.bin") {
		t.Fatalf("output = %q, want binary file excluded", step.ActionOutput)
	}
}

func TestGrepFilesIncludeFilter(t *testing.T) {
	t.Parallel()

	toolCtx := newGrepContext(t)
	step := tooltest.Invoke(t, toolCtx, plugin{}, `{"pattern":"needle","include":"*.md"}`)
	if step.Status != toolscore.StepStatusOK {
		t.Fatalf("step.Status = %q (error %q), want ok", step.Status, step.Error)
	}
	if !strings.Contains(step.ActionOutput, "docs/guide.md") {
		t.Fatalf("output = %q, want markdown match", step.ActionOutput)
	}
	if strings.Contains(step.ActionOutput, "main.go") {
		t.Fatalf("output = %q, want go file excluded by include filter", step.ActionOutput)
	}
}

func TestGrepFilesScopedToSubdirectory(t *testing.T) {
	t.Parallel()

	toolCtx := newGrepContext(t)
	step := tooltest.Invoke(t, toolCtx, plugin{}, `{"pattern":"needle","path":"docs"}`)
	if step.Status != toolscore.StepStatusOK {
		t.Fatalf("step.Status = %q (error %q), want ok", step.Status, step.Error)
	}
	if !strings.Contains(step.ActionOutput, "docs/guide.md") {
		t.Fatalf("output = %q, want match under docs", step.ActionOutput)
	}
	if strings.Contains(step.ActionOutput, "main.go") {
		t.Fatalf("output = %q, want files outside docs excluded", step.ActionOutput)
	}
}

func TestGrepFilesNoMatches(t *testing.T) {
	t.Parallel()

	toolCtx := newGrepContext(t)
	step := tooltest.Invoke(t, toolCtx, plugin{}, `{"pattern":"absent-token"}`)
	if !strings.Contains(step.ActionOutput, "No matching files found.") {
		t.Fatalf("output = %q, want no-match notice", step.ActionOutput)
	}
}

func TestGrepFilesRejections(t *testing.T) {
	t.Parallel()

	toolCtx := newGrepContext(t)
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty pattern", input: `{"pattern":"  "}`, want: "pattern must not be empty"},
		{name: "invalid regex", input: `{"pattern":"["}`, want: "invalid pattern"},
		{name: "escape attempt", input: `{"pattern":"x","path":"../"}`, want: "sandbox"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			step := tooltest.Invoke(t, toolCtx, plugin{}, test.input)
			if step.Status != toolscore.StepStatusPolicyError {
				t.Fatalf("step.Status = %q, want policy error", step.Status)
			}
			if !strings.Contains(step.Error, test.want) {
				t.Fatalf("step.Error = %q, want substring %q", step.Error, test.want)
			}
		})
	}
}
