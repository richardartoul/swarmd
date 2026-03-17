package readwebpage

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
	toolregistry "github.com/richardartoul/swarmd/pkg/tools/registry"
	"golang.org/x/net/html"
)

const toolName = "read_web_page"

var registerOnce sync.Once

type args struct {
	URL          string `json:"url"`
	Format       string `json:"format"`
	IncludeLinks bool   `json:"include_links"`
}

type plugin struct{}

func init() {
	Register()
}

func Register() {
	registerOnce.Do(func() {
		toolregistry.MustRegister(plugin{}, toolregistry.RegistrationOptions{BuiltIn: true})
	})
}

func (plugin) Definition() toolscore.ToolDefinition {
	return toolscore.ToolDefinition{
		Name:        toolName,
		Description: "Fetches a web page and converts HTML to markdown with metadata.",
		Kind:        toolscore.ToolKindFunction,
		Parameters: toolscommon.ObjectSchema(
			map[string]any{
				"url":           toolscommon.StringSchema("HTTP or HTTPS URL to fetch."),
				"format":        toolscommon.StringSchema(`Optional output format. Use "markdown" (default) or "text".`),
				"include_links": toolscommon.BooleanSchema("When true, include a bounded list of outbound links in the result."),
			},
			"url",
		),
		RequiredArguments: []string{"url"},
		Examples: []string{
			`{"url":"https://example.com/docs","format":"markdown","include_links":true}`,
		},
		OutputNotes: "Returns metadata such as status, final URL, title, markdown body, and optionally extracted outbound links.",
		Interop: toolscommon.ToolInterop(
			toolName,
			toolscore.ToolBoundaryKindFunction,
			toolscore.ToolBoundaryKindFunction,
			toolName,
		),
		SafetyTags:      []string{"network", "read_only"},
		RequiresNetwork: true,
		ReadOnly:        true,
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
	targetURL, err := toolscommon.ValidateHTTPToolURL(args.URL)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	format := strings.ToLower(strings.TrimSpace(args.Format))
	if format == "" {
		format = "markdown"
	}
	if format != "markdown" && format != "text" {
		toolCtx.SetPolicyError(step, fmt.Errorf("format must be \"markdown\" or \"text\""))
		return nil
	}

	timeout := toolscommon.BoundedDurationMillis(0, defaultReadWebPageTimeout, toolCtx.StepTimeout())
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	resp, body, truncated, err := executeRequest(ctx, toolCtx, http.MethodGet, targetURL.String(), nil, "", true, timeout, defaultReadWebPageBytes)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	defer resp.Body.Close()

	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	title := ""
	var markdown string
	var textContent string
	var links []string
	if doc, err := html.Parse(strings.NewReader(string(body))); err == nil {
		title = htmlDocumentTitle(doc)
		if format == "text" {
			textContent = toolscommon.CollapseWhitespace(toolscommon.NodeTextContent(doc))
		} else {
			markdown, err = htmltomarkdown.ConvertString(string(body))
			if err != nil {
				toolCtx.SetPolicyError(step, fmt.Errorf("convert HTML to markdown: %w", err))
				return nil
			}
			markdown = strings.TrimSpace(markdown)
		}
		if args.IncludeLinks {
			links = collectDocumentLinks(doc, maxReadWebPageLinks)
		}
	} else {
		if format == "text" {
			textContent = strings.TrimSpace(string(body))
		} else {
			markdown = strings.TrimSpace(string(body))
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "URL: %s\n", targetURL.String())
	fmt.Fprintf(&b, "Final URL: %s\n", resp.Request.URL.String())
	fmt.Fprintf(&b, "Status: %s\n", resp.Status)
	if contentType != "" {
		fmt.Fprintf(&b, "Content-Type: %s\n", contentType)
	}
	if title != "" {
		fmt.Fprintf(&b, "Title: %s\n", title)
	}
	if format == "text" {
		b.WriteString("Content:\n")
		b.WriteString(strings.TrimSpace(textContent))
		b.WriteString("\n")
	} else {
		b.WriteString("Markdown:\n")
		b.WriteString(strings.TrimSpace(markdown))
		b.WriteString("\n")
	}
	if truncated {
		b.WriteString("Body was truncated.\n")
	}
	if len(links) > 0 {
		b.WriteString("Links:\n")
		for idx, link := range links {
			fmt.Fprintf(&b, "%d|%s\n", idx+1, link)
		}
	}
	toolCtx.SetOutput(step, b.String())
	return nil
}

func executeRequest(ctx context.Context, toolCtx toolscore.ToolContext, method, rawURL string, headers map[string]string, body string, followRedirects bool, timeout time.Duration, maxBodyBytes int64) (*http.Response, []byte, bool, error) {
	client := toolCtx.HTTPClient(toolscore.ToolHTTPClientOptions{
		ConnectTimeout:  toolscommon.DefaultHTTPConnectTimeout,
		FollowRedirects: followRedirects,
	})
	if client == nil {
		return nil, nil, false, fmt.Errorf("HTTP client factory is not configured")
	}
	if followRedirects {
		toolscommon.WrapHTTPRedirectLimit(client, toolscommon.MaxHTTPRedirects)
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, strings.NewReader(body))
	if err != nil {
		return nil, nil, false, err
	}
	req.Header.Set("User-Agent", toolscommon.DefaultToolHTTPUserAgent)
	for name, value := range headers {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, nil, false, fmt.Errorf("request headers must not include empty names")
		}
		req.Header.Set(name, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, false, err
	}
	bodyBytes, truncated, err := toolscommon.ReadHTTPBodyLimited(resp.Body, maxBodyBytes)
	if err != nil {
		return resp, nil, false, err
	}
	return resp, bodyBytes, truncated, nil
}
