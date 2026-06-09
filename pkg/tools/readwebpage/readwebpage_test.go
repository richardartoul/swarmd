package readwebpage

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
	"github.com/richardartoul/swarmd/pkg/tools/internal/tooltest"
)

const testPageHTML = `<!DOCTYPE html>
<html>
<head><title>Demo Page</title></head>
<body>
<h1>Welcome</h1>
<p>Some <strong>bold</strong> prose.</p>
<a href="https://example.com/one">first</a>
<a href="https://example.com/two">second</a>
</body>
</html>`

func newPageServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, testPageHTML)
	}))
	t.Cleanup(server.Close)
	return server
}

func newWebContext(t *testing.T) *tooltest.Context {
	t.Helper()
	toolCtx := tooltest.NewContext(t)
	toolCtx.Transport = http.DefaultTransport
	return toolCtx
}

func TestReadWebPageMarkdown(t *testing.T) {
	t.Parallel()

	server := newPageServer(t)
	toolCtx := newWebContext(t)
	step := tooltest.Invoke(t, toolCtx, plugin{}, fmt.Sprintf(`{"url":%q}`, server.URL))
	if step.Status != toolscore.StepStatusOK {
		t.Fatalf("step.Status = %q (error %q), want ok", step.Status, step.Error)
	}
	for _, want := range []string{
		"Title: Demo Page",
		"Status: 200 OK",
		"Markdown:",
		"# Welcome",
		"**bold**",
	} {
		if !strings.Contains(step.ActionOutput, want) {
			t.Fatalf("output = %q, want substring %q", step.ActionOutput, want)
		}
	}
	if strings.Contains(step.ActionOutput, "Links:") {
		t.Fatalf("output = %q, want links omitted unless requested", step.ActionOutput)
	}
}

func TestReadWebPageTextWithLinks(t *testing.T) {
	t.Parallel()

	server := newPageServer(t)
	toolCtx := newWebContext(t)
	step := tooltest.Invoke(t, toolCtx, plugin{}, fmt.Sprintf(`{"url":%q,"format":"text","include_links":true}`, server.URL))
	if step.Status != toolscore.StepStatusOK {
		t.Fatalf("step.Status = %q (error %q), want ok", step.Status, step.Error)
	}
	for _, want := range []string{
		"Content:",
		"Welcome",
		"Links:",
		"https://example.com/one",
		"https://example.com/two",
	} {
		if !strings.Contains(step.ActionOutput, want) {
			t.Fatalf("output = %q, want substring %q", step.ActionOutput, want)
		}
	}
	if strings.Contains(step.ActionOutput, "<h1>") {
		t.Fatalf("output = %q, want HTML tags stripped in text mode", step.ActionOutput)
	}
}

func TestReadWebPageRejections(t *testing.T) {
	t.Parallel()

	server := newPageServer(t)
	toolCtx := newWebContext(t)
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty url", input: `{"url":""}`, want: "url must not be empty"},
		{name: "bad scheme", input: `{"url":"ftp://example.com"}`, want: "scheme must be http or https"},
		{name: "bad format", input: fmt.Sprintf(`{"url":%q,"format":"pdf"}`, server.URL), want: `format must be "markdown" or "text"`},
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
