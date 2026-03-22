package websearch

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/richardartoul/swarmd/pkg/sh/interp"
	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
	toolregistry "github.com/richardartoul/swarmd/pkg/tools/registry"
	"golang.org/x/net/html"
)

const (
	toolName              = "web_search"
	defaultWebSearchLimit = 5
	maxWebSearchLimit     = 10
)

var registerOnce sync.Once

type args struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

type plugin struct{}

// DuckDuckGoBackend is the default HTML-based backend used by the web_search tool.
type DuckDuckGoBackend struct {
	SearchURL string
}

func init() {
	Register()
}

func Register() {
	registerOnce.Do(func() {
		toolregistry.MustRegister(plugin{}, toolregistry.RegistrationOptions{BuiltIn: true})
	})
}

func NewDuckDuckGoBackend() toolscore.WebSearchBackend {
	return DuckDuckGoBackend{SearchURL: defaultDuckDuckGoSearchURL}
}

// NewGoogleBackend is kept for backward compatibility and now returns the DuckDuckGo backend.
func NewGoogleBackend() toolscore.WebSearchBackend {
	return NewDuckDuckGoBackend()
}

func (plugin) Definition() toolscore.ToolDefinition {
	return toolscore.ToolDefinition{
		Name:        toolName,
		Description: "Searches the public web through the runtime-owned search backend.",
		Kind:        toolscore.ToolKindFunction,
		Parameters: toolscommon.ObjectSchema(
			map[string]any{
				"query": toolscommon.StringSchema("Search query text."),
				"limit": toolscommon.NumberSchema("Maximum number of results to return."),
			},
			"query",
		),
		RequiredArguments: []string{"query"},
		Examples: []string{
			`{"query":"golang context package docs","limit":5}`,
		},
		OutputNotes: "Returns numbered `title|url` pairs with optional snippet lines from a normalized runtime-owned search backend.",
		Interop: toolscommon.ToolInterop(
			toolName,
			toolscore.ToolBoundaryKindWebSearch,
			toolscore.ToolBoundaryKindFunction,
			toolName,
		),
		SafetyTags:   []string{"network"},
		NetworkScope: toolscore.ToolNetworkScopeGlobal,
		ReadOnly:     true,
	}
}

func (plugin) NewHandler(config toolscore.ConfiguredTool) (toolscore.ToolHandler, error) {
	if err := toolscommon.ValidateNoToolConfig(toolName, config.Config); err != nil {
		return nil, err
	}
	return toolscore.ToolHandlerFunc(handle), nil
}

func handle(ctx context.Context, toolCtx toolscore.ToolContext, step *toolscore.Step, call *toolscore.ToolAction) error {
	args, err := toolscore.DecodeToolInput[args](call.Input)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		toolCtx.SetPolicyError(step, fmt.Errorf("query must not be empty"))
		return nil
	}

	limit := toolscommon.ClampInt(args.Limit, defaultWebSearchLimit, maxWebSearchLimit)
	timeout := toolscommon.BoundedDurationMillis(0, toolscommon.DefaultHTTPRequestTimeout, toolCtx.StepTimeout())
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	response, err := toolCtx.SearchWeb(ctx, query, limit)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Provider: %s\n", toolscommon.FirstNonEmptyString(response.Provider, defaultWebSearchProvider))
	fmt.Fprintf(&b, "Query: %s\n", query)
	if len(response.Results) == 0 {
		b.WriteString("No results found.\n")
		toolCtx.SetOutput(step, b.String())
		return nil
	}
	for idx, result := range response.Results {
		fmt.Fprintf(&b, "%d|%s|%s\n", idx+1, strings.TrimSpace(result.Title), strings.TrimSpace(result.URL))
		if snippet := strings.TrimSpace(result.Snippet); snippet != "" {
			fmt.Fprintf(&b, "  %s\n", snippet)
		}
	}
	toolCtx.SetOutput(step, b.String())
	return nil
}

func (b DuckDuckGoBackend) Search(ctx context.Context, clientFactory interp.HTTPClientFactory, query string, limit int) (toolscore.WebSearchResponse, error) {
	baseURL := strings.TrimSpace(b.SearchURL)
	if baseURL == "" {
		baseURL = defaultDuckDuckGoSearchURL
	}
	searchURL, err := url.Parse(baseURL)
	if err != nil {
		return toolscore.WebSearchResponse{}, fmt.Errorf("parse DuckDuckGo search url: %w", err)
	}
	values := searchURL.Query()
	values.Set("q", query)
	values.Set("kl", "us-en")
	searchURL.RawQuery = values.Encode()

	client := clientFactory.NewClient(interp.HTTPClientOptions{
		ConnectTimeout:  toolscommon.DefaultHTTPConnectTimeout,
		FollowRedirects: true,
	})
	toolscommon.WrapHTTPRedirectLimit(client, toolscommon.MaxHTTPRedirects)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL.String(), nil)
	if err != nil {
		return toolscore.WebSearchResponse{}, err
	}
	req.Header.Set("User-Agent", toolscommon.DefaultToolHTTPUserAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := client.Do(req)
	if err != nil {
		return toolscore.WebSearchResponse{}, err
	}
	defer resp.Body.Close()
	body, _, err := toolscommon.ReadHTTPBodyLimited(resp.Body, toolscommon.DefaultHTTPResponseBytes)
	if err != nil {
		return toolscore.WebSearchResponse{}, err
	}
	results, err := parseDuckDuckGoSearchResults(body, limit)
	if err != nil {
		return toolscore.WebSearchResponse{}, err
	}
	return toolscore.WebSearchResponse{
		Provider: defaultWebSearchProvider,
		Query:    query,
		Results:  results,
	}, nil
}

func parseDuckDuckGoSearchResults(body []byte, limit int) ([]toolscore.WebSearchResult, error) {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("parse DuckDuckGo search response: %w", err)
	}
	results := make([]toolscore.WebSearchResult, 0, limit)
	seen := make(map[string]struct{}, limit)
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node == nil || len(results) >= limit {
			return
		}
		if node.Type == html.ElementNode && node.Data == "a" && hasHTMLClass(node, "result__a") {
			candidateURL, ok := duckDuckGoResultTarget(toolscommon.HTMLAttr(node, "href"))
			if ok {
				if _, seenURL := seen[candidateURL]; !seenURL {
					title := toolscommon.CollapseWhitespace(toolscommon.NodeTextContent(node))
					if title != "" {
						snippet := ""
						if container := nearestAncestorWithClass(node, "result__body"); container != nil {
							if snippetNode := firstDescendantWithClass(container, "result__snippet"); snippetNode != nil {
								snippet = toolscommon.CollapseWhitespace(toolscommon.NodeTextContent(snippetNode))
							}
						}
						results = append(results, toolscore.WebSearchResult{
							Title:   title,
							URL:     candidateURL,
							Snippet: snippet,
						})
						seen[candidateURL] = struct{}{}
					}
				}
			}
		}
		for child := node.FirstChild; child != nil && len(results) < limit; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return results, nil
}

func duckDuckGoResultTarget(rawHref string) (string, bool) {
	rawHref = strings.TrimSpace(rawHref)
	if rawHref == "" {
		return "", false
	}
	switch {
	case strings.HasPrefix(rawHref, "/l/?"):
		rawHref = "https://duckduckgo.com" + rawHref
	case strings.HasPrefix(rawHref, "//"):
		rawHref = "https:" + rawHref
	}
	parsed, err := url.Parse(rawHref)
	if err != nil {
		return "", false
	}
	if isDuckDuckGoRedirectURL(parsed) {
		target := strings.TrimSpace(parsed.Query().Get("uddg"))
		if _, err := toolscommon.ValidateHTTPToolURL(target); err != nil {
			return "", false
		}
		return target, true
	}
	if parsed, err := toolscommon.ValidateHTTPToolURL(rawHref); err == nil {
		return parsed.String(), true
	}
	return "", false
}

func isDuckDuckGoRedirectURL(target *url.URL) bool {
	if target == nil {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(target.Hostname()))
	return (host == "duckduckgo.com" || strings.HasSuffix(host, ".duckduckgo.com")) &&
		(target.Path == "/l/" || target.Path == "/l")
}

func ParseDuckDuckGoSearchResults(body []byte, limit int) ([]toolscore.WebSearchResult, error) {
	return parseDuckDuckGoSearchResults(body, limit)
}

func DuckDuckGoResultTarget(rawHref string) (string, bool) {
	return duckDuckGoResultTarget(rawHref)
}
