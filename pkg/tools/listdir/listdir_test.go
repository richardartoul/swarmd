package listdir

import (
	"path/filepath"
	"strings"
	"testing"

	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
	"github.com/richardartoul/swarmd/pkg/tools/internal/tooltest"
)

func newPopulatedContext(t *testing.T) *tooltest.Context {
	t.Helper()
	toolCtx := tooltest.NewContext(t)
	toolCtx.WriteFile(t, "alpha.txt", "a")
	toolCtx.WriteFile(t, "beta.txt", "b")
	toolCtx.WriteFile(t, "sub/nested.txt", "n")
	toolCtx.WriteFile(t, "sub/deep/leaf.txt", "l")
	return toolCtx
}

func TestListDirDepthOne(t *testing.T) {
	t.Parallel()

	toolCtx := newPopulatedContext(t)
	step := tooltest.Invoke(t, toolCtx, plugin{}, `{"dir_path":"."}`)
	if step.Status != toolscore.StepStatusOK {
		t.Fatalf("step.Status = %q (error %q), want ok", step.Status, step.Error)
	}
	for _, want := range []string{"Showing entries 1-3 of 3", "file|alpha.txt", "file|beta.txt", "dir|sub"} {
		if !strings.Contains(step.ActionOutput, want) {
			t.Fatalf("output = %q, want substring %q", step.ActionOutput, want)
		}
	}
	if strings.Contains(step.ActionOutput, "nested.txt") {
		t.Fatalf("output = %q, want depth-one listing without nested entries", step.ActionOutput)
	}
}

func TestListDirRecursesToRequestedDepth(t *testing.T) {
	t.Parallel()

	toolCtx := newPopulatedContext(t)
	step := tooltest.Invoke(t, toolCtx, plugin{}, `{"dir_path":".","depth":3}`)
	if step.Status != toolscore.StepStatusOK {
		t.Fatalf("step.Status = %q (error %q), want ok", step.Status, step.Error)
	}
	for _, want := range []string{"file|sub/nested.txt", "file|sub/deep/leaf.txt"} {
		if !strings.Contains(step.ActionOutput, want) {
			t.Fatalf("output = %q, want substring %q", step.ActionOutput, want)
		}
	}
}

func TestListDirPagination(t *testing.T) {
	t.Parallel()

	toolCtx := newPopulatedContext(t)
	step := tooltest.Invoke(t, toolCtx, plugin{}, `{"dir_path":".","offset":2,"limit":1}`)
	if step.Status != toolscore.StepStatusOK {
		t.Fatalf("step.Status = %q (error %q), want ok", step.Status, step.Error)
	}
	if !strings.Contains(step.ActionOutput, "Showing entries 2-2 of 3") {
		t.Fatalf("output = %q, want single-entry window", step.ActionOutput)
	}

	step = tooltest.Invoke(t, toolCtx, plugin{}, `{"dir_path":".","offset":99}`)
	if !strings.Contains(step.ActionOutput, "No entries in this range.") {
		t.Fatalf("output = %q, want out-of-range notice", step.ActionOutput)
	}
}

func TestListDirEmptyDirectory(t *testing.T) {
	t.Parallel()

	toolCtx := tooltest.NewContext(t)
	if err := toolCtx.FS.MkdirAll(filepath.Join(toolCtx.Root, "empty"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	step := tooltest.Invoke(t, toolCtx, plugin{}, `{"dir_path":"empty"}`)
	if !strings.Contains(step.ActionOutput, "No entries found.") {
		t.Fatalf("output = %q, want empty-directory notice", step.ActionOutput)
	}
}

func TestListDirRejections(t *testing.T) {
	t.Parallel()

	toolCtx := newPopulatedContext(t)
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty path", input: `{"dir_path":""}`, want: "dir_path must not be empty"},
		{name: "missing directory", input: `{"dir_path":"missing"}`, want: "missing"},
		{name: "file path", input: `{"dir_path":"alpha.txt"}`, want: "is not a directory"},
		{name: "escape attempt", input: `{"dir_path":"../"}`, want: "sandbox"},
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
