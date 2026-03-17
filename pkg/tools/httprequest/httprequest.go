package httprequest

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
	toolregistry "github.com/richardartoul/swarmd/pkg/tools/registry"
)

const (
	toolName                = "http_request"
	maxHTTPRequestTimeout   = 60 * time.Second
	maxHTTPRequestBodyBytes = 256 << 10
	maxHTTPRequestHeaders   = 64
)

var registerOnce sync.Once

type args struct {
	Method          string            `json:"method"`
	URL             string            `json:"url"`
	Headers         map[string]string `json:"headers"`
	Body            string            `json:"body"`
	FollowRedirects bool              `json:"follow_redirects"`
	TimeoutMS       int               `json:"timeout_ms"`
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
		Description: "Makes a direct HTTP request for API-style interactions.",
		Kind:        toolscore.ToolKindFunction,
		Parameters: toolscommon.ObjectSchema(
			map[string]any{
				"method":           toolscommon.StringSchema("HTTP method. Defaults to GET."),
				"url":              toolscommon.StringSchema("HTTP or HTTPS URL to request."),
				"headers":          toolscommon.StringMapSchema("Optional HTTP request headers."),
				"body":             toolscommon.StringSchema("Optional UTF-8 request body."),
				"follow_redirects": toolscommon.BooleanSchema("Whether to follow redirects."),
				"timeout_ms":       toolscommon.NumberSchema("Optional request timeout in milliseconds."),
			},
			"url",
		),
		RequiredArguments: []string{"url"},
		Examples: []string{
			`{"method":"GET","url":"https://api.example.com/v1/users","follow_redirects":true}`,
			`{"method":"POST","url":"https://api.example.com/v1/users","headers":{"Content-Type":"application/json"},"body":"{\"name\":\"Ada\"}"}`,
		},
		OutputNotes: "Returns status, final URL, response headers, and a bounded UTF-8 response body.",
		Interop: toolscommon.ToolInterop(
			toolName,
			toolscore.ToolBoundaryKindFunction,
			toolscore.ToolBoundaryKindFunction,
			toolName,
		),
		SafetyTags:      []string{"network"},
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
	method := strings.ToUpper(strings.TrimSpace(args.Method))
	if method == "" {
		method = http.MethodGet
	}
	timeout := toolscommon.BoundedDurationMillis(args.TimeoutMS, toolscommon.DefaultHTTPRequestTimeout, toolCtx.StepTimeout())
	if timeout > maxHTTPRequestTimeout {
		timeout = maxHTTPRequestTimeout
	}
	resp, body, truncated, err := executeRequest(ctx, toolCtx, method, targetURL.String(), args.Headers, args.Body, args.FollowRedirects, timeout, toolscommon.DefaultHTTPResponseBytes)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	defer resp.Body.Close()

	var b strings.Builder
	fmt.Fprintf(&b, "Method: %s\n", method)
	fmt.Fprintf(&b, "URL: %s\n", targetURL.String())
	fmt.Fprintf(&b, "Final URL: %s\n", resp.Request.URL.String())
	fmt.Fprintf(&b, "Status: %s\n", resp.Status)
	if contentType := strings.TrimSpace(resp.Header.Get("Content-Type")); contentType != "" {
		fmt.Fprintf(&b, "Content-Type: %s\n", contentType)
	}
	if headerText := toolscommon.FormatHTTPHeaderMap(resp.Header); headerText != "" {
		b.WriteString("Headers:\n")
		b.WriteString(headerText)
	}
	if len(body) > 0 {
		bodyText := toolscommon.FormatHTTPBody(body)
		if bodyText != "" {
			b.WriteString("Body:\n")
			b.WriteString(bodyText)
			b.WriteString("\n")
		}
	}
	if truncated {
		b.WriteString("Body was truncated.\n")
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
	if len(body) > maxHTTPRequestBodyBytes {
		return nil, nil, false, fmt.Errorf("request body exceeded the limit of %d bytes", maxHTTPRequestBodyBytes)
	}
	if len(headers) > maxHTTPRequestHeaders {
		return nil, nil, false, fmt.Errorf("request header count exceeded the limit of %d", maxHTTPRequestHeaders)
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
