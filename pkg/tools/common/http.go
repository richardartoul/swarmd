package common

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	DefaultHTTPConnectTimeout = 10 * time.Second
	DefaultHTTPRequestTimeout = 20 * time.Second
	DefaultHTTPResponseBytes  = 1 << 20
	MaxHTTPRedirects          = 5
	DefaultToolHTTPUserAgent  = "Mozilla/5.0 (compatible; swarmd/1.0; +https://example.invalid/agent)"
)

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
