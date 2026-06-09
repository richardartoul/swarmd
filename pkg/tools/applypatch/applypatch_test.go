package applypatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
)

// patchToolContext is a minimal toolscore.ToolContext backed by a real
// sandbox filesystem rooted at a test temp directory.
type patchToolContext struct {
	toolscore.UnimplementedToolContext
	fs   *sandbox.FS
	root string
}

func newPatchToolContext(t *testing.T) patchToolContext {
	t.Helper()
	root := t.TempDir()
	fs, err := sandbox.NewFS(root)
	if err != nil {
		t.Fatalf("sandbox.NewFS() error = %v", err)
	}
	resolvedRoot, err := fs.Getwd()
	if err != nil {
		t.Fatalf("fs.Getwd() error = %v", err)
	}
	return patchToolContext{fs: fs, root: resolvedRoot}
}

func (c patchToolContext) WorkingDir() string             { return c.root }
func (c patchToolContext) FileSystem() sandbox.FileSystem { return c.fs }

func (c patchToolContext) ResolvePath(path string) (string, error) {
	return sandbox.ResolvePath(c.fs, c.root, path)
}

func applyPatchInput(t *testing.T, toolCtx patchToolContext, patch string) toolscore.Step {
	t.Helper()
	step := toolscore.Step{}
	err := handle(context.Background(), toolCtx, &step, &toolscore.ToolAction{
		Name:  toolName,
		Kind:  toolscore.ToolKindCustom,
		Input: patch,
	})
	if err != nil {
		t.Fatalf("handle() error = %v", err)
	}
	return step
}

func writeTestFile(t *testing.T, toolCtx patchToolContext, name, content string) string {
	t.Helper()
	path := filepath.Join(toolCtx.root, name)
	if err := toolCtx.fs.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := toolCtx.fs.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func readTestFile(t *testing.T, toolCtx patchToolContext, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(toolCtx.root, name))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	return string(data)
}

func TestApplyPatchAddFile(t *testing.T) {
	t.Parallel()

	toolCtx := newPatchToolContext(t)
	step := applyPatchInput(t, toolCtx, strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: notes/hello.txt",
		"+first line",
		"+second line",
		"*** End Patch",
	}, "\n"))
	if step.Status != toolscore.StepStatusOK {
		t.Fatalf("step.Status = %q (error %q), want ok", step.Status, step.Error)
	}
	if got, want := readTestFile(t, toolCtx, "notes/hello.txt"), "first line\nsecond line\n"; got != want {
		t.Fatalf("file content = %q, want %q", got, want)
	}
	if !strings.Contains(step.ActionOutput, "add|") {
		t.Fatalf("step.ActionOutput = %q, want add summary", step.ActionOutput)
	}
}

func TestApplyPatchAddFileRejectsExisting(t *testing.T) {
	t.Parallel()

	toolCtx := newPatchToolContext(t)
	writeTestFile(t, toolCtx, "existing.txt", "already here\n")
	step := applyPatchInput(t, toolCtx, strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: existing.txt",
		"+overwrite",
		"*** End Patch",
	}, "\n"))
	if step.Status != toolscore.StepStatusPolicyError {
		t.Fatalf("step.Status = %q, want policy error for existing file", step.Status)
	}
	if got := readTestFile(t, toolCtx, "existing.txt"); got != "already here\n" {
		t.Fatalf("file content = %q, want original preserved", got)
	}
}

func TestApplyPatchUpdateFile(t *testing.T) {
	t.Parallel()

	toolCtx := newPatchToolContext(t)
	writeTestFile(t, toolCtx, "app.txt", "alpha\nbeta\ngamma\n")
	step := applyPatchInput(t, toolCtx, strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: app.txt",
		"@@",
		" alpha",
		"-beta",
		"+BETA",
		" gamma",
		"*** End Patch",
	}, "\n"))
	if step.Status != toolscore.StepStatusOK {
		t.Fatalf("step.Status = %q (error %q), want ok", step.Status, step.Error)
	}
	if got, want := readTestFile(t, toolCtx, "app.txt"), "alpha\nBETA\ngamma\n"; got != want {
		t.Fatalf("file content = %q, want %q", got, want)
	}
}

func TestApplyPatchUpdateFileMultipleHunks(t *testing.T) {
	t.Parallel()

	toolCtx := newPatchToolContext(t)
	writeTestFile(t, toolCtx, "multi.txt", "one\ntwo\nthree\nfour\nfive\n")
	step := applyPatchInput(t, toolCtx, strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: multi.txt",
		"@@",
		"-one",
		"+ONE",
		"@@",
		"-four",
		"+FOUR",
		"*** End Patch",
	}, "\n"))
	if step.Status != toolscore.StepStatusOK {
		t.Fatalf("step.Status = %q (error %q), want ok", step.Status, step.Error)
	}
	if got, want := readTestFile(t, toolCtx, "multi.txt"), "ONE\ntwo\nthree\nFOUR\nfive\n"; got != want {
		t.Fatalf("file content = %q, want %q", got, want)
	}
}

func TestApplyPatchUpdatePreservesMissingTrailingNewline(t *testing.T) {
	t.Parallel()

	toolCtx := newPatchToolContext(t)
	writeTestFile(t, toolCtx, "raw.txt", "alpha\nbeta") // no trailing newline
	step := applyPatchInput(t, toolCtx, strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: raw.txt",
		"@@",
		"-alpha",
		"+ALPHA",
		"*** End Patch",
	}, "\n"))
	if step.Status != toolscore.StepStatusOK {
		t.Fatalf("step.Status = %q (error %q), want ok", step.Status, step.Error)
	}
	if got, want := readTestFile(t, toolCtx, "raw.txt"), "ALPHA\nbeta"; got != want {
		t.Fatalf("file content = %q, want %q", got, want)
	}
}

func TestApplyPatchUpdateRejectsUnmatchedHunk(t *testing.T) {
	t.Parallel()

	toolCtx := newPatchToolContext(t)
	writeTestFile(t, toolCtx, "app.txt", "alpha\n")
	step := applyPatchInput(t, toolCtx, strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: app.txt",
		"@@",
		"-does not exist",
		"+replacement",
		"*** End Patch",
	}, "\n"))
	if step.Status != toolscore.StepStatusPolicyError {
		t.Fatalf("step.Status = %q, want policy error for unmatched hunk", step.Status)
	}
	if got := readTestFile(t, toolCtx, "app.txt"); got != "alpha\n" {
		t.Fatalf("file content = %q, want original preserved", got)
	}
}

func TestApplyPatchDeleteFile(t *testing.T) {
	t.Parallel()

	toolCtx := newPatchToolContext(t)
	writeTestFile(t, toolCtx, "obsolete.txt", "old data\n")
	step := applyPatchInput(t, toolCtx, strings.Join([]string{
		"*** Begin Patch",
		"*** Delete File: obsolete.txt",
		"*** End Patch",
	}, "\n"))
	if step.Status != toolscore.StepStatusOK {
		t.Fatalf("step.Status = %q (error %q), want ok", step.Status, step.Error)
	}
	if !strings.Contains(step.ActionOutput, "delete|") {
		t.Fatalf("step.ActionOutput = %q, want delete summary", step.ActionOutput)
	}
	if _, err := os.Stat(filepath.Join(toolCtx.root, "obsolete.txt")); !os.IsNotExist(err) {
		t.Fatalf("Stat() error = %v, want file removed", err)
	}
}

func TestApplyPatchDeleteFileRejectsMissingFile(t *testing.T) {
	t.Parallel()

	toolCtx := newPatchToolContext(t)
	step := applyPatchInput(t, toolCtx, strings.Join([]string{
		"*** Begin Patch",
		"*** Delete File: never-existed.txt",
		"*** End Patch",
	}, "\n"))
	if step.Status != toolscore.StepStatusPolicyError {
		t.Fatalf("step.Status = %q, want policy error for missing file", step.Status)
	}
}

func TestApplyPatchDeleteFileRejectsDirectory(t *testing.T) {
	t.Parallel()

	toolCtx := newPatchToolContext(t)
	if err := toolCtx.fs.MkdirAll(filepath.Join(toolCtx.root, "subdir"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	step := applyPatchInput(t, toolCtx, strings.Join([]string{
		"*** Begin Patch",
		"*** Delete File: subdir",
		"*** End Patch",
	}, "\n"))
	if step.Status != toolscore.StepStatusPolicyError {
		t.Fatalf("step.Status = %q, want policy error for directory", step.Status)
	}
	if _, err := os.Stat(filepath.Join(toolCtx.root, "subdir")); err != nil {
		t.Fatalf("Stat() error = %v, want directory preserved", err)
	}
}

func TestApplyPatchMixedOperations(t *testing.T) {
	t.Parallel()

	toolCtx := newPatchToolContext(t)
	writeTestFile(t, toolCtx, "update-me.txt", "old\n")
	writeTestFile(t, toolCtx, "delete-me.txt", "bye\n")
	step := applyPatchInput(t, toolCtx, strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: new.txt",
		"+fresh",
		"*** Update File: update-me.txt",
		"@@",
		"-old",
		"+new",
		"*** Delete File: delete-me.txt",
		"*** End Patch",
	}, "\n"))
	if step.Status != toolscore.StepStatusOK {
		t.Fatalf("step.Status = %q (error %q), want ok", step.Status, step.Error)
	}
	if got := readTestFile(t, toolCtx, "new.txt"); got != "fresh\n" {
		t.Fatalf("new.txt = %q, want %q", got, "fresh\n")
	}
	if got := readTestFile(t, toolCtx, "update-me.txt"); got != "new\n" {
		t.Fatalf("update-me.txt = %q, want %q", got, "new\n")
	}
	if _, err := os.Stat(filepath.Join(toolCtx.root, "delete-me.txt")); !os.IsNotExist(err) {
		t.Fatalf("Stat() error = %v, want delete-me.txt removed", err)
	}
}

func TestApplyPatchCRLFInputIsAccepted(t *testing.T) {
	t.Parallel()

	toolCtx := newPatchToolContext(t)
	step := applyPatchInput(t, toolCtx, strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: crlf.txt",
		"+windows line",
		"*** End Patch",
	}, "\r\n"))
	if step.Status != toolscore.StepStatusOK {
		t.Fatalf("step.Status = %q (error %q), want ok", step.Status, step.Error)
	}
	if got := readTestFile(t, toolCtx, "crlf.txt"); got != "windows line\n" {
		t.Fatalf("crlf.txt = %q, want %q", got, "windows line\n")
	}
}

func TestApplyPatchRejectsMalformedPatches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		patch string
	}{
		{name: "empty", patch: ""},
		{name: "missing begin", patch: "*** Update File: x.txt\n*** End Patch"},
		{name: "missing end", patch: "*** Begin Patch\n*** Add File: x.txt\n+line"},
		{name: "content after end", patch: "*** Begin Patch\n*** End Patch\nextra"},
		{name: "unknown directive", patch: "*** Begin Patch\n*** Move File: x.txt\n*** End Patch"},
		{name: "add without lines", patch: "*** Begin Patch\n*** Add File: x.txt\n*** End Patch"},
		{name: "delete without path", patch: "*** Begin Patch\n*** Delete File: \n*** End Patch"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			toolCtx := newPatchToolContext(t)
			step := applyPatchInput(t, toolCtx, test.patch)
			if step.Status != toolscore.StepStatusParseError {
				t.Fatalf("step.Status = %q (error %q), want parse error", step.Status, step.Error)
			}
		})
	}
}

func TestApplyPatchRejectsEscapingSandboxRoot(t *testing.T) {
	t.Parallel()

	toolCtx := newPatchToolContext(t)
	step := applyPatchInput(t, toolCtx, strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: ../outside.txt",
		"+escape attempt",
		"*** End Patch",
	}, "\n"))
	if step.Status != toolscore.StepStatusPolicyError {
		t.Fatalf("step.Status = %q (error %q), want policy error", step.Status, step.Error)
	}
	if _, err := os.Stat(filepath.Join(toolCtx.root, "..", "outside.txt")); !os.IsNotExist(err) {
		t.Fatalf("Stat() error = %v, want no file written outside sandbox", err)
	}
}
