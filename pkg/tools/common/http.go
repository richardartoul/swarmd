package common

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
)

const (
	DefaultHTTPConnectTimeout = 10 * time.Second
	DefaultHTTPRequestTimeout = 20 * time.Second
	DefaultHTTPResponseBytes  = 1 << 20
	MaxHTTPRedirects          = 5
	DefaultToolHTTPUserAgent  = "Mozilla/5.0 (compatible; swarmd/1.0; +https://example.invalid/agent)"
)

// ToolHTTPHeader is one outbound request header. A header named "Host"
// overrides the request's Host instead of being set as a regular header.
type ToolHTTPHeader struct {
	Name  string
	Value string
}

// ToolHTTPRequest describes one outbound HTTP request made on behalf of a
// structured tool.
type ToolHTTPRequest struct {
	// Method defaults to GET when empty.
	Method  string
	URL     string
	Headers []ToolHTTPHeader
	Body    string
	// FollowRedirects enables redirect following, bounded by MaxHTTPRedirects.
	FollowRedirects bool
	// Timeout bounds the whole request when positive.
	Timeout time.Duration
	// MaxBodyBytes bounds the drained response body; zero applies
	// DefaultHTTPResponseBytes.
	MaxBodyBytes int64
}

// ToolHTTPResponse is a fully drained HTTP response. The underlying body has
// already been read and closed; callers never manage the connection.
type ToolHTTPResponse struct {
	Status        string
	StatusCode    int
	Header        http.Header
	FinalURL      string
	Body          []byte
	BodyTruncated bool
}

// DoToolHTTPRequest performs one tool-scoped HTTP request through the tool
// context's host-policy-enforcing client and returns a drained response.
//
// Owning the full request lifecycle here guarantees the response body is
// closed on every path, including read failures; returning a live
// *http.Response from a helper is how connection leaks happen.
func DoToolHTTPRequest(ctx context.Context, toolCtx toolscore.ToolContext, req ToolHTTPRequest) (ToolHTTPResponse, error) {
	client := toolCtx.HTTPClient(toolscore.ToolHTTPClientOptions{
		ConnectTimeout:  DefaultHTTPConnectTimeout,
		FollowRedirects: req.FollowRedirects,
	})
	if client == nil {
		return ToolHTTPResponse{}, fmt.Errorf("HTTP client factory is not configured")
	}
	if req.FollowRedirects {
		WrapHTTPRedirectLimit(client, MaxHTTPRedirects)
	}
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, req.URL, strings.NewReader(req.Body))
	if err != nil {
		return ToolHTTPResponse{}, err
	}
	httpReq.Header.Set("User-Agent", DefaultToolHTTPUserAgent)
	for _, header := range req.Headers {
		name := strings.TrimSpace(header.Name)
		if name == "" {
			return ToolHTTPResponse{}, fmt.Errorf("request headers must not include empty names")
		}
		if strings.EqualFold(name, "Host") {
			httpReq.Host = header.Value
			continue
		}
		httpReq.Header.Set(name, header.Value)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return ToolHTTPResponse{}, err
	}
	defer resp.Body.Close()

	body, truncated, err := ReadHTTPBodyLimited(resp.Body, req.MaxBodyBytes)
	if err != nil {
		return ToolHTTPResponse{}, fmt.Errorf("read response body: %w", err)
	}
	finalURL := req.URL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	return ToolHTTPResponse{
		Status:        resp.Status,
		StatusCode:    resp.StatusCode,
		Header:        resp.Header,
		FinalURL:      finalURL,
		Body:          body,
		BodyTruncated: truncated,
	}, nil
}

func ValidateHTTPToolURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("url must not be empty")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("url scheme must be http or https")
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return nil, fmt.Errorf("url host must not be empty")
	}
	return parsed, nil
}

func ReadHTTPBodyLimited(body io.Reader, limit int64) ([]byte, bool, error) {
	if limit <= 0 {
		limit = DefaultHTTPResponseBytes
	}
	reader := io.LimitReader(body, limit+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > limit {
		return data[:limit], true, nil
	}
	return data, false, nil
}

func WrapHTTPRedirectLimit(client *http.Client, limit int) {
	if client == nil || limit <= 0 {
		return
	}
	previous := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= limit {
			return fmt.Errorf("stopped after %d redirects", limit)
		}
		if previous != nil {
			return previous(req, via)
		}
		return nil
	}
}

func FormatHTTPHeaderMap(headers http.Header) string {
	if len(headers) == 0 {
		return ""
	}
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		fmt.Fprintf(&b, "%s: %s\n", name, strings.Join(headers.Values(name), ", "))
	}
	return b.String()
}

func FormatHTTPBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	if !utf8.Valid(body) {
		return fmt.Sprintf("[non-UTF-8 body omitted, %d bytes]", len(body))
	}
	text := string(body)
	if strings.ContainsRune(text, '\x00') {
		return fmt.Sprintf("[binary body omitted, %d bytes]", len(body))
	}
	return strings.TrimSpace(text)
}
