package httprequest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
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

type headerArg struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type headerList []headerArg

func (h *headerList) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	switch {
	case len(trimmed) == 0, string(trimmed) == "null":
		*h = nil
		return nil
	case trimmed[0] == '[':
		var headers []headerArg
		if err := json.Unmarshal(trimmed, &headers); err != nil {
			return err
		}
		*h = headerList(headers)
		return nil
	case trimmed[0] == '{':
		var legacy map[string]string
		if err := json.Unmarshal(trimmed, &legacy); err != nil {
			return err
		}
		names := make([]string, 0, len(legacy))
		for name := range legacy {
			names = append(names, name)
		}
		sort.Strings(names)
		headers := make([]headerArg, 0, len(names))
		for _, name := range names {
			headers = append(headers, headerArg{
				Name:  name,
				Value: legacy[name],
			})
		}
		*h = headerList(headers)
		return nil
	default:
		return fmt.Errorf("headers must be an array of {name, value} objects")
	}
}

type args struct {
	Method          string     `json:"method"`
	URL             string     `json:"url"`
	Headers         headerList `json:"headers"`
	Body            string     `json:"body"`
	FollowRedirects bool       `json:"follow_redirects"`
	TimeoutMS       int        `json:"timeout_ms"`
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
				"method": toolscommon.StringSchema("HTTP method. Defaults to GET."),
				"url":    toolscommon.StringSchema("HTTP or HTTPS URL to request."),
				"headers": map[string]any{
					"type":        "array",
					"description": "Optional HTTP request headers as an array of {name, value} objects.",
					"items": toolscommon.ObjectSchema(map[string]any{
						"name":  toolscommon.StringSchema("HTTP header name."),
						"value": toolscommon.StringSchema("HTTP header value."),
					}, "name", "value"),
				},
				"body":             toolscommon.StringSchema("Optional UTF-8 request body."),
				"follow_redirects": toolscommon.BooleanSchema("Whether to follow redirects."),
				"timeout_ms":       toolscommon.NumberSchema("Optional request timeout in milliseconds."),
			},
			"url",
		),
		RequiredArguments: []string{"url"},
		Examples: []string{
			`{"method":"GET","url":"https://api.example.com/v1/users","follow_redirects":true}`,
			`{"method":"POST","url":"https://api.example.com/v1/users","headers":[{"name":"Content-Type","value":"application/json"}],"body":"{\"name\":\"Ada\"}"}`,
		},
		OutputNotes: "Returns status, final URL, response headers, and a bounded UTF-8 response body.",
		Interop: toolscommon.ToolInterop(
			toolName,
			toolscore.ToolBoundaryKindFunction,
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
	targetURL, err := toolscommon.ValidateHTTPToolURL(args.URL)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	method := strings.ToUpper(strings.TrimSpace(args.Method))
	if method == "" {
		method = http.MethodGet
	}
	if len(args.Body) > maxHTTPRequestBodyBytes {
		toolCtx.SetPolicyError(step, fmt.Errorf("request body exceeded the limit of %d bytes", maxHTTPRequestBodyBytes))
		return nil
	}
	if len(args.Headers) > maxHTTPRequestHeaders {
		toolCtx.SetPolicyError(step, fmt.Errorf("request header count exceeded the limit of %d", maxHTTPRequestHeaders))
		return nil
	}
	timeout := toolscommon.BoundedDurationMillis(args.TimeoutMS, toolscommon.DefaultHTTPRequestTimeout, toolCtx.StepTimeout())
	if timeout > maxHTTPRequestTimeout {
		timeout = maxHTTPRequestTimeout
	}

	headers := make([]toolscommon.ToolHTTPHeader, 0, len(args.Headers))
	for _, header := range args.Headers {
		headers = append(headers, toolscommon.ToolHTTPHeader{Name: header.Name, Value: header.Value})
	}
	resp, err := toolscommon.DoToolHTTPRequest(ctx, toolCtx, toolscommon.ToolHTTPRequest{
		Method:          method,
		URL:             targetURL.String(),
		Headers:         headers,
		Body:            args.Body,
		FollowRedirects: args.FollowRedirects,
		Timeout:         timeout,
		MaxBodyBytes:    toolscommon.DefaultHTTPResponseBytes,
	})
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Method: %s\n", method)
	fmt.Fprintf(&b, "URL: %s\n", targetURL.String())
	fmt.Fprintf(&b, "Final URL: %s\n", resp.FinalURL)
	fmt.Fprintf(&b, "Status: %s\n", resp.Status)
	if contentType := strings.TrimSpace(resp.Header.Get("Content-Type")); contentType != "" {
		fmt.Fprintf(&b, "Content-Type: %s\n", contentType)
	}
	if headerText := toolscommon.FormatHTTPHeaderMap(resp.Header); headerText != "" {
		b.WriteString("Headers:\n")
		b.WriteString(headerText)
	}
	if len(resp.Body) > 0 {
		bodyText := toolscommon.FormatHTTPBody(resp.Body)
		if bodyText != "" {
			b.WriteString("Body:\n")
			b.WriteString(bodyText)
			b.WriteString("\n")
		}
	}
	if resp.BodyTruncated {
		b.WriteString("Body was truncated.\n")
	}
	toolCtx.SetOutput(step, b.String())
	return nil
}
