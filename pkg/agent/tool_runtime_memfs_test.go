package agent_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/richardartoul/swarmd/pkg/agent"
	"github.com/richardartoul/swarmd/pkg/sh/memfs"
)

func TestToolRuntimeSupportsMemFS(t *testing.T) {
	t.Parallel()

	fsys, err := memfs.New("/workspace")
	if err != nil {
		t.Fatalf("memfs.New() error = %v", err)
	}
	if err := fsys.MkdirAll("/workspace/fixture/sub", 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := fsys.WriteFile("/workspace/note.txt", []byte("before\nold\nafter\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(note.txt) error = %v", err)
	}
	if err := fsys.WriteFile("/workspace/fixture/one.txt", []byte("Needle here\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(one.txt) error = %v", err)
	}
	if err := fsys.WriteFile("/workspace/fixture/sub/two.txt", []byte("another Needle\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(two.txt) error = %v", err)
	}

	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: note.txt",
		"@@",
		" before",
		"-old",
		"+new",
		" after",
		"*** End Patch",
	}, "\n")
	a := newAgent(t, agent.Config{
		FileSystem: fsys,
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				tool(agent.ToolNameReadFile, agent.ToolKindFunction, `{"file_path":"note.txt","offset":1,"limit":10}`),
				tool(agent.ToolNameListDir, agent.ToolKindFunction, `{"dir_path":"fixture","depth":2}`),
				tool(agent.ToolNameGrepFiles, agent.ToolKindFunction, `{"pattern":"Needle","path":"fixture","include":"*.txt"}`),
				tool(agent.ToolNameApplyPatch, agent.ToolKindCustom, patch),
				finish("done"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "memfs-tools"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	if len(result.Steps) != 4 {
		t.Fatalf("len(result.Steps) = %d, want 4", len(result.Steps))
	}
	if got := result.Steps[0].ActionOutput; !strings.Contains(got, "old") {
		t.Fatalf("read_file output = %q, want note contents", got)
	}
	if got := result.Steps[1].ActionOutput; !strings.Contains(got, "one.txt") || !strings.Contains(got, "sub/") {
		t.Fatalf("list_dir output = %q, want fixture entries", got)
	}
	if got := result.Steps[2].ActionOutput; !strings.Contains(got, "Needle") || !strings.Contains(got, "one.txt") {
		t.Fatalf("grep_files output = %q, want grep matches", got)
	}
	if got := result.Steps[3].Status; got != agent.StepStatusOK {
		t.Fatalf("apply_patch status = %q, want %q", got, agent.StepStatusOK)
	}

	file, err := fsys.Open("/workspace/note.txt")
	if err != nil {
		t.Fatalf("Open(note.txt) error = %v", err)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("ReadAll(note.txt) error = %v", err)
	}
	if got := string(data); got != "before\nnew\nafter\n" {
		t.Fatalf("note.txt = %q, want %q", got, "before\nnew\nafter\n")
	}
}
