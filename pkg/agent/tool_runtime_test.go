package agent_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/richardartoul/swarmd/pkg/agent"
	"github.com/richardartoul/swarmd/pkg/sh/interp"
)

func TestHandleTriggerExecutesInjectedWebSearchTool(t *testing.T) {
	t.Parallel()

	backend := &fakeWebSearchBackend{
		response: agent.WebSearchResponse{
			Provider: "fake",
			Results: []agent.WebSearchResult{{
				Title:   "Example Docs",
				URL:     "https://example.com/docs",
				Snippet: "Documentation homepage",
			}},
		},
	}
	a := newAgent(t, agent.Config{
		Root:             t.TempDir(),
		NetworkEnabled:   true,
		WebSearchBackend: backend,
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				tool(agent.ToolNameWebSearch, agent.ToolKindFunction, `{"query":"example docs","limit":1}`),
				finish("done"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "web-search"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	if backend.query != "example docs" {
		t.Fatalf("backend query = %q, want %q", backend.query, "example docs")
	}
	if backend.limit != 1 {
		t.Fatalf("backend limit = %d, want 1", backend.limit)
	}
	if got := result.Steps[0].Status; got != agent.StepStatusOK {
		t.Fatalf("step status = %q, want %q", got, agent.StepStatusOK)
	}
	if got := result.Steps[0].ActionOutput; !strings.Contains(got, "Example Docs") || !strings.Contains(got, "https://example.com/docs") {
		t.Fatalf("step output = %q, want formatted search result", got)
	}
}

func TestHandleTriggerExecutesHTTPRequestTool(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(httpTestHandler(`{"ok":true}`, "application/json"))
	defer server.Close()

	a := newAgent(t, agent.Config{
		Root:          t.TempDir(),
		NetworkDialer: interp.OSNetworkDialer{},
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				tool(agent.ToolNameHTTPRequest, agent.ToolKindFunction, `{"url":"`+server.URL+`","method":"GET","follow_redirects":true}`),
				finish("done"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "http-request"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	output := result.Steps[0].ActionOutput
	if !strings.Contains(output, "Status: 200 OK") {
		t.Fatalf("step output = %q, want HTTP status", output)
	}
	if !strings.Contains(output, `{"ok":true}`) {
		t.Fatalf("step output = %q, want JSON body", output)
	}
}

func TestHandleTriggerHTTPRequestToolRejectsDisallowedHost(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(httpTestHandler(`{"ok":true}`, "application/json"))
	defer server.Close()

	dialer, err := interp.NewAllowlistNetworkDialer(interp.OSNetworkDialer{}, []interp.HostMatcher{{
		Glob: "example.com",
	}})
	if err != nil {
		t.Fatalf("NewAllowlistNetworkDialer() error = %v", err)
	}

	a := newAgent(t, agent.Config{
		Root:          t.TempDir(),
		NetworkDialer: dialer,
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				tool(agent.ToolNameHTTPRequest, agent.ToolKindFunction, `{"url":"`+server.URL+`","method":"GET","follow_redirects":true}`),
				finish("done"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "http-request-denied"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	step := result.Steps[0]
	if step.Status != agent.StepStatusPolicyError {
		t.Fatalf("step.Status = %q, want %q", step.Status, agent.StepStatusPolicyError)
	}
	if !strings.Contains(step.Error, "network host not allowed") {
		t.Fatalf("step.Error = %q, want allowlist failure", step.Error)
	}
}

func TestHandleTriggerExecutesReadWebPageTool(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(httpTestHandler(`<!doctype html><html><head><title>Example Page</title></head><body><h1>Example Page</h1><p>Hello <a href="https://example.com/link">world</a>.</p></body></html>`, "text/html; charset=utf-8"))
	defer server.Close()

	a := newAgent(t, agent.Config{
		Root:          t.TempDir(),
		NetworkDialer: interp.OSNetworkDialer{},
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				tool(agent.ToolNameReadWebPage, agent.ToolKindFunction, `{"url":"`+server.URL+`","format":"markdown","include_links":true}`),
				finish("done"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "read-web-page"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	output := result.Steps[0].ActionOutput
	if !strings.Contains(output, "Title: Example Page") {
		t.Fatalf("step output = %q, want page title", output)
	}
	if !strings.Contains(output, "Markdown:") || !strings.Contains(output, "Example Page") {
		t.Fatalf("step output = %q, want markdown content", output)
	}
	if !strings.Contains(output, "Links:") || !strings.Contains(output, "https://example.com/link") {
		t.Fatalf("step output = %q, want extracted links", output)
	}
}

func TestHandleTriggerReadWebPageToolRejectsDisallowedHost(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(httpTestHandler(`<!doctype html><html><body>blocked</body></html>`, "text/html; charset=utf-8"))
	defer server.Close()

	dialer, err := interp.NewAllowlistNetworkDialer(interp.OSNetworkDialer{}, []interp.HostMatcher{{
		Glob: "example.com",
	}})
	if err != nil {
		t.Fatalf("NewAllowlistNetworkDialer() error = %v", err)
	}

	a := newAgent(t, agent.Config{
		Root:          t.TempDir(),
		NetworkDialer: dialer,
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				tool(agent.ToolNameReadWebPage, agent.ToolKindFunction, `{"url":"`+server.URL+`","format":"markdown","include_links":true}`),
				finish("done"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "read-web-page-denied"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	step := result.Steps[0]
	if step.Status != agent.StepStatusPolicyError {
		t.Fatalf("step.Status = %q, want %q", step.Status, agent.StepStatusPolicyError)
	}
	if !strings.Contains(step.Error, "network host not allowed") {
		t.Fatalf("step.Error = %q, want allowlist failure", step.Error)
	}
}

func TestHandleTriggerWebSearchToolRejectsDisallowedHost(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(httpTestHandler(`ok`, "text/plain"))
	defer server.Close()

	dialer, err := interp.NewAllowlistNetworkDialer(interp.OSNetworkDialer{}, []interp.HostMatcher{{
		Glob: "example.com",
	}})
	if err != nil {
		t.Fatalf("NewAllowlistNetworkDialer() error = %v", err)
	}

	a := newAgent(t, agent.Config{
		Root:          t.TempDir(),
		NetworkDialer: dialer,
		WebSearchBackend: &fetchingWebSearchBackend{
			url: server.URL,
		},
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				tool(agent.ToolNameWebSearch, agent.ToolKindFunction, `{"query":"example docs","limit":1}`),
				finish("done"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "web-search-denied"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	step := result.Steps[0]
	if step.Status != agent.StepStatusPolicyError {
		t.Fatalf("step.Status = %q, want %q", step.Status, agent.StepStatusPolicyError)
	}
	if !strings.Contains(step.Error, "network host not allowed") {
		t.Fatalf("step.Error = %q, want allowlist failure", step.Error)
	}
}

func TestHandleTriggerExecutesApplyPatchTool(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("before\nold\nafter\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(note.txt) error = %v", err)
	}
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: note.txt",
		"@@",
		" before",
		"-old",
		"+new",
		" after",
		"*** Add File: created.txt",
		"+hello",
		"*** End Patch",
	}, "\n")

	a := newAgent(t, agent.Config{
		Root: root,
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				tool(agent.ToolNameApplyPatch, agent.ToolKindCustom, patch),
				finish("done"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "apply-patch"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	if got := result.Steps[0].Status; got != agent.StepStatusOK {
		t.Fatalf("step status = %q, want %q", got, agent.StepStatusOK)
	}
	updated, err := os.ReadFile(filepath.Join(root, "note.txt"))
	if err != nil {
		t.Fatalf("os.ReadFile(note.txt) error = %v", err)
	}
	if got := string(updated); got != "before\nnew\nafter\n" {
		t.Fatalf("note.txt = %q, want patched contents", got)
	}
	created, err := os.ReadFile(filepath.Join(root, "created.txt"))
	if err != nil {
		t.Fatalf("os.ReadFile(created.txt) error = %v", err)
	}
	if got := string(created); got != "hello\n" {
		t.Fatalf("created.txt = %q, want added file contents", got)
	}
}

func TestHandleTriggerTruncatesToolOutputToConfiguredLimit(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte(strings.Repeat("line\n", 80)), 0o644); err != nil {
		t.Fatalf("os.WriteFile(note.txt) error = %v", err)
	}

	a := newAgent(t, agent.Config{
		Root:           root,
		MaxOutputBytes: 96,
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				tool(agent.ToolNameReadFile, agent.ToolKindFunction, `{"file_path":"note.txt","offset":1,"limit":80}`),
				finish("done"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "truncated-tool-output"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	step := result.Steps[0]
	if !step.ActionOutputTruncated {
		t.Fatal("step.ActionOutputTruncated = false, want true")
	}
	if got := len(step.ActionOutput); got > 96 {
		t.Fatalf("len(step.ActionOutput) = %d, want <= 96", got)
	}
}

func TestHandleTriggerReadFileReportsNoLinesForPastEOFRange(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := osWriteFile(filepath.Join(root, "note.txt"), []byte("one\ntwo\nthree\n")); err != nil {
		t.Fatalf("osWriteFile(note.txt) error = %v", err)
	}

	a := newAgent(t, agent.Config{
		Root: root,
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				tool(agent.ToolNameReadFile, agent.ToolKindFunction, `{"file_path":"note.txt","offset":5,"limit":2}`),
				finish("done"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "read-file-past-eof"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	output := result.Steps[0].ActionOutput
	if !strings.Contains(output, "No lines in this range.") {
		t.Fatalf("step output = %q, want empty range message", output)
	}
	if strings.Contains(output, "Showing lines") {
		t.Fatalf("step output = %q, want no inverted range header", output)
	}
}

func TestHandleTriggerReadFileKeepsValidEndBoundaryRange(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := osWriteFile(filepath.Join(root, "note.txt"), []byte("one\ntwo\nthree\n")); err != nil {
		t.Fatalf("osWriteFile(note.txt) error = %v", err)
	}

	a := newAgent(t, agent.Config{
		Root: root,
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				tool(agent.ToolNameReadFile, agent.ToolKindFunction, `{"file_path":"note.txt","offset":3,"limit":10}`),
				finish("done"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "read-file-end-boundary"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	output := result.Steps[0].ActionOutput
	if !strings.Contains(output, "Showing lines 3-3 of 3") {
		t.Fatalf("step output = %q, want clipped end-boundary header", output)
	}
	if !strings.Contains(output, "3|three") {
		t.Fatalf("step output = %q, want final line", output)
	}
}

func TestHandleTriggerReadFileRejectsIndentationArgsInSliceMode(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := osWriteFile(filepath.Join(root, "note.txt"), []byte("one\ntwo\nthree\n")); err != nil {
		t.Fatalf("osWriteFile(note.txt) error = %v", err)
	}

	a := newAgent(t, agent.Config{
		Root: root,
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				tool(agent.ToolNameReadFile, agent.ToolKindFunction, `{"file_path":"note.txt","mode":"slice","indentation":{"anchor_line":2}}`),
				finish("done"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "read-file-slice-indentation"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	step := result.Steps[0]
	if step.Status != agent.StepStatusPolicyError {
		t.Fatalf("step.Status = %q, want %q", step.Status, agent.StepStatusPolicyError)
	}
	if !strings.Contains(step.Error, `indentation requires mode "indentation"`) {
		t.Fatalf("step.Error = %q, want indentation mode validation error", step.Error)
	}
}

func TestHandleTriggerReadFileRejectsInvalidMode(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := osWriteFile(filepath.Join(root, "note.txt"), []byte("one\ntwo\nthree\n")); err != nil {
		t.Fatalf("osWriteFile(note.txt) error = %v", err)
	}

	a := newAgent(t, agent.Config{
		Root: root,
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				tool(agent.ToolNameReadFile, agent.ToolKindFunction, `{"file_path":"note.txt","mode":"blocks"}`),
				finish("done"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "read-file-invalid-mode"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	step := result.Steps[0]
	if step.Status != agent.StepStatusPolicyError {
		t.Fatalf("step.Status = %q, want %q", step.Status, agent.StepStatusPolicyError)
	}
	if !strings.Contains(step.Error, `mode must be "slice" or "indentation"`) {
		t.Fatalf("step.Error = %q, want invalid mode error", step.Error)
	}
}

func TestHandleTriggerListDirReportsNoEntriesForPastEOFRange(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fixtureDir := filepath.Join(root, "fixture")
	if err := os.Mkdir(fixtureDir, 0o755); err != nil {
		t.Fatalf("os.Mkdir(fixture) error = %v", err)
	}
	if err := osWriteFile(filepath.Join(fixtureDir, "alpha.txt"), []byte("alpha\n")); err != nil {
		t.Fatalf("osWriteFile(alpha.txt) error = %v", err)
	}
	if err := osWriteFile(filepath.Join(fixtureDir, "beta.txt"), []byte("beta\n")); err != nil {
		t.Fatalf("osWriteFile(beta.txt) error = %v", err)
	}

	a := newAgent(t, agent.Config{
		Root: root,
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				tool(agent.ToolNameListDir, agent.ToolKindFunction, `{"dir_path":"fixture","offset":5,"limit":10,"depth":1}`),
				finish("done"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "list-dir-past-eof"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	output := result.Steps[0].ActionOutput
	if !strings.Contains(output, "No entries in this range.") {
		t.Fatalf("step output = %q, want empty range message", output)
	}
	if strings.Contains(output, "Showing entries") {
		t.Fatalf("step output = %q, want no inverted range header", output)
	}
}

func TestHandleTriggerListDirKeepsValidEndBoundaryRange(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fixtureDir := filepath.Join(root, "fixture")
	if err := os.Mkdir(fixtureDir, 0o755); err != nil {
		t.Fatalf("os.Mkdir(fixture) error = %v", err)
	}
	if err := osWriteFile(filepath.Join(fixtureDir, "alpha.txt"), []byte("alpha\n")); err != nil {
		t.Fatalf("osWriteFile(alpha.txt) error = %v", err)
	}
	if err := osWriteFile(filepath.Join(fixtureDir, "beta.txt"), []byte("beta\n")); err != nil {
		t.Fatalf("osWriteFile(beta.txt) error = %v", err)
	}

	a := newAgent(t, agent.Config{
		Root: root,
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				tool(agent.ToolNameListDir, agent.ToolKindFunction, `{"dir_path":"fixture","offset":2,"limit":10,"depth":1}`),
				finish("done"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "list-dir-end-boundary"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	output := result.Steps[0].ActionOutput
	if !strings.Contains(output, "Showing entries 2-2 of 2") {
		t.Fatalf("step output = %q, want clipped end-boundary header", output)
	}
	if !strings.Contains(output, "2|file|beta.txt") {
		t.Fatalf("step output = %q, want second directory entry", output)
	}
}

func TestHandleTriggerListDirRecursivePaginationPreservesTotalCount(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fixtureDir := filepath.Join(root, "fixture")
	if err := os.Mkdir(fixtureDir, 0o755); err != nil {
		t.Fatalf("os.Mkdir(fixture) error = %v", err)
	}
	if err := osWriteFile(filepath.Join(fixtureDir, "alpha.txt"), []byte("alpha\n")); err != nil {
		t.Fatalf("osWriteFile(alpha.txt) error = %v", err)
	}
	if err := os.Mkdir(filepath.Join(fixtureDir, "sub"), 0o755); err != nil {
		t.Fatalf("os.Mkdir(sub) error = %v", err)
	}
	if err := osWriteFile(filepath.Join(fixtureDir, "sub", "child.txt"), []byte("child\n")); err != nil {
		t.Fatalf("osWriteFile(sub/child.txt) error = %v", err)
	}

	a := newAgent(t, agent.Config{
		Root: root,
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				tool(agent.ToolNameListDir, agent.ToolKindFunction, `{"dir_path":"fixture","offset":2,"limit":1,"depth":2}`),
				finish("done"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "list-dir-recursive-page"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	output := result.Steps[0].ActionOutput
	if !strings.Contains(output, "Showing entries 2-2 of 3") {
		t.Fatalf("step output = %q, want full recursive total", output)
	}
	if !strings.Contains(output, "2|dir|sub") {
		t.Fatalf("step output = %q, want requested recursive page entry", output)
	}
}

func TestHandleTriggerGrepFilesSupportsBraceExpandedInclude(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for path, contents := range map[string]string{
		"match.go":  "Needle\n",
		"notes.md":  "Needle\n",
		"plain.txt": "Needle\n",
		"skip.js":   "Needle\n",
	} {
		if err := osWriteFile(filepath.Join(root, path), []byte(contents)); err != nil {
			t.Fatalf("osWriteFile(%s) error = %v", path, err)
		}
	}

	a := newAgent(t, agent.Config{
		Root: root,
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				tool(agent.ToolNameGrepFiles, agent.ToolKindFunction, `{"pattern":"Needle","path":".","include":"*.{go,md,txt}"}`),
				finish("done"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "grep-files-braces"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	output := result.Steps[0].ActionOutput
	for _, path := range []string{"match.go", "notes.md", "plain.txt"} {
		want := filepath.ToSlash(filepath.Join(root, path))
		if !strings.Contains(output, want) {
			t.Fatalf("step output = %q, want %q", output, want)
		}
	}
	if strings.Contains(output, filepath.ToSlash(filepath.Join(root, "skip.js"))) {
		t.Fatalf("step output = %q, want include filter to exclude .js files", output)
	}
}

func TestHandleTriggerGrepFilesKeepsSimpleIncludeGlob(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := osWriteFile(filepath.Join(root, "match.go"), []byte("Needle\n")); err != nil {
		t.Fatalf("osWriteFile(match.go) error = %v", err)
	}
	if err := osWriteFile(filepath.Join(root, "notes.md"), []byte("Needle\n")); err != nil {
		t.Fatalf("osWriteFile(notes.md) error = %v", err)
	}

	a := newAgent(t, agent.Config{
		Root: root,
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				tool(agent.ToolNameGrepFiles, agent.ToolKindFunction, `{"pattern":"Needle","path":".","include":"*.go"}`),
				finish("done"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "grep-files-simple-glob"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	output := result.Steps[0].ActionOutput
	if !strings.Contains(output, filepath.ToSlash(filepath.Join(root, "match.go"))) {
		t.Fatalf("step output = %q, want .go match", output)
	}
	if strings.Contains(output, filepath.ToSlash(filepath.Join(root, "notes.md"))) {
		t.Fatalf("step output = %q, want .md excluded by simple include glob", output)
	}
}

func TestHandleTriggerGrepFilesMalformedBraceIncludeDoesNotWidenMatches(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := osWriteFile(filepath.Join(root, "match.go"), []byte("Needle\n")); err != nil {
		t.Fatalf("osWriteFile(match.go) error = %v", err)
	}
	if err := osWriteFile(filepath.Join(root, "notes.md"), []byte("Needle\n")); err != nil {
		t.Fatalf("osWriteFile(notes.md) error = %v", err)
	}

	a := newAgent(t, agent.Config{
		Root: root,
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				tool(agent.ToolNameGrepFiles, agent.ToolKindFunction, `{"pattern":"Needle","path":".","include":"*.{go,md"}`),
				finish("done"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "grep-files-malformed-braces"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	output := result.Steps[0].ActionOutput
	if !strings.Contains(output, "No matching files found.") {
		t.Fatalf("step output = %q, want empty result for malformed brace include", output)
	}
}

func TestHandleTriggerHTTPRequestEnforcesRedirectLimit(t *testing.T) {
	t.Parallel()

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		step := 0
		if trimmed := strings.Trim(strings.TrimSpace(r.URL.Path), "/"); trimmed != "" {
			if parsed, err := strconv.Atoi(trimmed); err == nil {
				step = parsed
			}
		}
		if step < 6 {
			http.Redirect(w, r, server.URL+"/"+strconv.Itoa(step+1), http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("done"))
	}))
	defer server.Close()

	a := newAgent(t, agent.Config{
		Root:          t.TempDir(),
		NetworkDialer: interp.OSNetworkDialer{},
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				tool(agent.ToolNameHTTPRequest, agent.ToolKindFunction, `{"url":"`+server.URL+`/0","follow_redirects":true}`),
				finish("done"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "redirect-limit"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	step := result.Steps[0]
	if step.Status != agent.StepStatusPolicyError {
		t.Fatalf("step.Status = %q, want %q", step.Status, agent.StepStatusPolicyError)
	}
	if !strings.Contains(step.Error, "redirect") {
		t.Fatalf("step.Error = %q, want redirect limit error", step.Error)
	}
}

type fakeWebSearchBackend struct {
	response agent.WebSearchResponse
	query    string
	limit    int
}

func (b *fakeWebSearchBackend) Search(_ context.Context, _ interp.HTTPClientFactory, query string, limit int) (agent.WebSearchResponse, error) {
	b.query = query
	b.limit = limit
	return b.response, nil
}

type fetchingWebSearchBackend struct {
	url string
}

func (b *fetchingWebSearchBackend) Search(ctx context.Context, factory interp.HTTPClientFactory, query string, limit int) (agent.WebSearchResponse, error) {
	_ = query
	_ = limit
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.url, nil)
	if err != nil {
		return agent.WebSearchResponse{}, err
	}
	resp, err := factory.NewClient(interp.HTTPClientOptions{FollowRedirects: true}).Do(req)
	if err != nil {
		return agent.WebSearchResponse{}, err
	}
	defer resp.Body.Close()
	return agent.WebSearchResponse{Provider: "fetch"}, nil
}

func tool(name string, kind agent.ToolKind, input string) agent.Decision {
	return agent.Decision{
		Tool: &agent.ToolAction{
			Name:  name,
			Kind:  kind,
			Input: input,
		},
	}
}

func httpTestHandler(body string, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		_, _ = w.Write([]byte(body))
	}
}
