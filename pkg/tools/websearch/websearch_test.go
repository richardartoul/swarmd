package websearch

import (
	"net/url"
	"testing"
)

const duckDuckGoFixture = `<!DOCTYPE html>
<html>
<body>
<div class="result results_links results_links_deep web-result">
  <div class="result__body links_main links_deep">
    <h2 class="result__title">
      <a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Ffirst&amp;rut=abc">First Result</a>
    </h2>
    <a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Ffirst">Snippet for the <b>first</b> result.</a>
  </div>
</div>
<div class="result">
  <div class="result__body">
    <h2 class="result__title">
      <a class="result__a" href="https://example.org/direct">Second Result</a>
    </h2>
    <a class="result__snippet">Second snippet.</a>
  </div>
</div>
<div class="result">
  <div class="result__body">
    <h2 class="result__title">
      <a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Ffirst">Duplicate Of First</a>
    </h2>
  </div>
</div>
<div class="result">
  <div class="result__body">
    <h2 class="result__title">
      <a class="result__a" href="javascript:alert(1)">Bad Scheme</a>
    </h2>
  </div>
</div>
</body>
</html>`

func TestParseDuckDuckGoSearchResults(t *testing.T) {
	t.Parallel()

	results, err := parseDuckDuckGoSearchResults([]byte(duckDuckGoFixture), 10)
	if err != nil {
		t.Fatalf("parseDuckDuckGoSearchResults() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2 (deduplicated, bad schemes dropped): %+v", len(results), results)
	}
	if results[0].Title != "First Result" {
		t.Fatalf("results[0].Title = %q, want %q", results[0].Title, "First Result")
	}
	if results[0].URL != "https://example.com/first" {
		t.Fatalf("results[0].URL = %q, want redirect target decoded", results[0].URL)
	}
	if results[0].Snippet != "Snippet for the first result." {
		t.Fatalf("results[0].Snippet = %q, want collapsed snippet text", results[0].Snippet)
	}
	if results[1].URL != "https://example.org/direct" {
		t.Fatalf("results[1].URL = %q, want direct link preserved", results[1].URL)
	}
}

func TestParseDuckDuckGoSearchResultsHonorsLimit(t *testing.T) {
	t.Parallel()

	results, err := parseDuckDuckGoSearchResults([]byte(duckDuckGoFixture), 1)
	if err != nil {
		t.Fatalf("parseDuckDuckGoSearchResults() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
}

func TestDuckDuckGoResultTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		href string
		want string
		ok   bool
	}{
		{
			name: "redirect link decodes uddg",
			href: "//duckduckgo.com/l/?uddg=" + url.QueryEscape("https://example.com/page?q=1"),
			want: "https://example.com/page?q=1",
			ok:   true,
		},
		{name: "relative redirect", href: "/l/?uddg=" + url.QueryEscape("https://example.com/x"), want: "https://example.com/x", ok: true},
		{name: "direct https", href: "https://example.org/a", want: "https://example.org/a", ok: true},
		{name: "javascript scheme rejected", href: "javascript:alert(1)"},
		{name: "redirect to non-http rejected", href: "/l/?uddg=" + url.QueryEscape("file:///etc/passwd")},
		{name: "empty", href: " "},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, ok := duckDuckGoResultTarget(test.href)
			if ok != test.ok || got != test.want {
				t.Fatalf("duckDuckGoResultTarget(%q) = (%q, %t), want (%q, %t)", test.href, got, ok, test.want, test.ok)
			}
		})
	}
}
