package agent

import (
	"testing"

	websearchtool "github.com/richardartoul/swarmd/pkg/tools/websearch"
)

func TestParseDuckDuckGoSearchResults(t *testing.T) {
	t.Parallel()

	body := []byte(`
<!doctype html>
<html>
  <body>
    <div class="result results_links results_links_deep web-result">
      <div class="links_main links_deep result__body">
        <h2 class="result__title">
          <a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fdocs&amp;rut=abc">Example Docs</a>
        </h2>
        <a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fdocs&amp;rut=abc">Useful documentation for examples.</a>
      </div>
    </div>
    <div class="result results_links results_links_deep web-result">
      <div class="links_main links_deep result__body">
        <h2 class="result__title">
          <a class="result__a" href="/l/?uddg=https%3A%2F%2Fexample.com%2Fdocs&amp;rut=def">Duplicate Example Docs</a>
        </h2>
        <a class="result__snippet" href="/l/?uddg=https%3A%2F%2Fexample.com%2Fdocs&amp;rut=def">Duplicate snippet.</a>
      </div>
    </div>
    <div class="result results_links results_links_deep web-result">
      <div class="links_main links_deep result__body">
        <h2 class="result__title">
          <a class="result__a" href="https://example.org/post">Example Post</a>
        </h2>
        <a class="result__snippet" href="https://example.org/post">Another search result snippet.</a>
      </div>
    </div>
  </body>
</html>
`)

	results, err := websearchtool.ParseDuckDuckGoSearchResults(body, 5)
	if err != nil {
		t.Fatalf("ParseDuckDuckGoSearchResults() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}

	if got := results[0]; got.Title != "Example Docs" || got.URL != "https://example.com/docs" || got.Snippet != "Useful documentation for examples." {
		t.Fatalf("results[0] = %#v, want title/url/snippet for example docs", got)
	}
	if got := results[1]; got.Title != "Example Post" || got.URL != "https://example.org/post" || got.Snippet != "Another search result snippet." {
		t.Fatalf("results[1] = %#v, want title/url/snippet for example post", got)
	}
}

func TestDuckDuckGoResultTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{
			name: "scheme-relative redirect",
			raw:  "//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fdocs",
			want: "https://example.com/docs",
			ok:   true,
		},
		{
			name: "relative redirect",
			raw:  "/l/?uddg=https%3A%2F%2Fexample.com%2Fdocs",
			want: "https://example.com/docs",
			ok:   true,
		},
		{
			name: "direct result URL",
			raw:  "https://example.org/post",
			want: "https://example.org/post",
			ok:   true,
		},
		{
			name: "reject non-http URL",
			raw:  "javascript:alert(1)",
			ok:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := websearchtool.DuckDuckGoResultTarget(tt.raw)
			if ok != tt.ok {
				t.Fatalf("DuckDuckGoResultTarget(%q) ok = %v, want %v", tt.raw, ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("DuckDuckGoResultTarget(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
