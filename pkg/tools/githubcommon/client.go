package githubcommon

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
)

type ClientConfig struct {
	Token      string
	BaseURL    string
	HTTPClient *http.Client
}

type Client struct {
	token      string
	baseURL    string
	httpClient *http.Client
}

type ResponseMeta struct {
	StatusCode  int
	Header      http.Header
	FinalURL    string
	Redirected  bool
	ContentType string
	SizeBytes   int64
}

type DownloadResult struct {
	Meta  ResponseMeta
	Files []FileReference
}

func NewClient(cfg ClientConfig) (*Client, error) {
	token := strings.TrimSpace(cfg.Token)
	if token == "" {
		return nil, fmt.Errorf("github client requires %s", GitHubTokenEnvVar)
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultGitHubAPIBaseURL
	}
	if _, err := url.Parse(baseURL); err != nil {
		return nil, fmt.Errorf("invalid GitHub base URL: %w", err)
	}
	if cfg.HTTPClient == nil {
		return nil, fmt.Errorf("GitHub client requires an HTTP client")
	}
	return &Client{
		token:      token,
		baseURL:    baseURL,
		httpClient: cfg.HTTPClient,
	}, nil
}

func NewClientFromLookup(lookupEnv func(string) string, httpClient *http.Client) (*Client, error) {
	if lookupEnv == nil {
		lookupEnv = func(string) string { return "" }
	}
	return NewClient(ClientConfig{
		Token:      lookupEnv(GitHubTokenEnvVar),
		BaseURL:    lookupEnv(GitHubAPIBaseURLEnvVar),
		HTTPClient: httpClient,
	})
}

func (c *Client) JSON(ctx context.Context, method, endpoint string, query url.Values, accept string, out any) (ResponseMeta, error) {
	req, requestURL, err := c.newRequest(ctx, method, endpoint, query, accept)
	if err != nil {
		return ResponseMeta{}, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ResponseMeta{}, fmt.Errorf("GitHub request failed: %w", err)
	}
	defer resp.Body.Close()

	body, truncated, err := toolscommon.ReadHTTPBodyLimited(resp.Body, DefaultJSONResponseBytes)
	if err != nil {
		return ResponseMeta{}, fmt.Errorf("read GitHub response: %w", err)
	}
	if truncated {
		return ResponseMeta{}, &APIError{
			Status:  resp.StatusCode,
			Code:    "response_too_large",
			Message: fmt.Sprintf("GitHub response exceeded %d bytes", DefaultJSONResponseBytes),
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ResponseMeta{}, classifyAPIError(resp, body)
	}
	if out != nil && len(body) > 0 {
		if err := json.Unmarshal(body, out); err != nil {
			return ResponseMeta{}, fmt.Errorf("decode GitHub response JSON: %w", err)
		}
	}
	return responseMeta(resp, requestURL, int64(len(body))), nil
}

func (c *Client) Download(
	ctx context.Context,
	endpoint string,
	query url.Values,
	accept string,
	relativePath string,
	description string,
	fsys sandbox.FileSystem,
	resolvePath func(string) (string, error),
	extractZip bool,
	extractRoot string,
) (DownloadResult, error) {
	req, requestURL, err := c.newRequest(ctx, http.MethodGet, endpoint, query, accept)
	if err != nil {
		return DownloadResult{}, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("GitHub download failed: %w", err)
	}
	defer resp.Body.Close()

	body, truncated, err := toolscommon.ReadHTTPBodyLimited(resp.Body, MaxDownloadBytes)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("read GitHub download body: %w", err)
	}
	if truncated {
		return DownloadResult{}, &APIError{
			Status:  resp.StatusCode,
			Code:    "download_too_large",
			Message: fmt.Sprintf("GitHub download exceeded %d bytes", MaxDownloadBytes),
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return DownloadResult{}, classifyAPIError(resp, body)
	}

	if err := writeFile(fsys, resolvePath, relativePath, body); err != nil {
		return DownloadResult{}, err
	}
	files := []FileReference{{
		Path:        strings.TrimSpace(relativePath),
		MimeType:    detectMimeType(relativePath, resp.Header.Get("Content-Type")),
		Description: strings.TrimSpace(description),
		SizeBytes:   int64(len(body)),
	}}
	if extractZip {
		extractedFiles, err := extractZipData(body, strings.TrimSpace(extractRoot), fsys, resolvePath)
		if err != nil {
			return DownloadResult{}, err
		}
		files = append(files, extractedFiles...)
	}
	return DownloadResult{
		Meta:  responseMeta(resp, requestURL, int64(len(body))),
		Files: files,
	}, nil
}

func EscapePathPreservingSlashes(value string) string {
	if value == "" {
		return ""
	}
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	parts := strings.Split(value, "/")
	escaped := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		escaped = append(escaped, url.PathEscape(part))
	}
	return strings.Join(escaped, "/")
}

func (c *Client) newRequest(ctx context.Context, method, endpoint string, query url.Values, accept string) (*http.Request, string, error) {
	target, err := c.buildURL(endpoint, query)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", toolscommon.FirstNonEmptyString(strings.TrimSpace(accept), "application/vnd.github+json"))
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-GitHub-Api-Version", GitHubAPIVersion)
	req.Header.Set("User-Agent", toolscommon.DefaultToolHTTPUserAgent)
	return req, target, nil
}

func (c *Client) buildURL(endpoint string, query url.Values) (string, error) {
	base, err := url.Parse(c.baseURL)
	if err != nil {
		return "", fmt.Errorf("parse GitHub base URL: %w", err)
	}
	endpoint = strings.TrimSpace(endpoint)
	endpoint = "/" + strings.TrimPrefix(endpoint, "/")
	base.Path = strings.TrimRight(base.Path, "/") + endpoint
	if len(query) > 0 {
		base.RawQuery = query.Encode()
	}
	return base.String(), nil
}

func (c *Client) URL(endpoint string, query url.Values) (string, error) {
	return c.buildURL(endpoint, query)
}

func responseMeta(resp *http.Response, requestURL string, sizeBytes int64) ResponseMeta {
	finalURL := ""
	redirected := false
	if resp != nil && resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
		redirected = finalURL != "" && finalURL != requestURL
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if semi := strings.Index(contentType, ";"); semi >= 0 {
		contentType = strings.TrimSpace(contentType[:semi])
	}
	return ResponseMeta{
		StatusCode:  resp.StatusCode,
		Header:      resp.Header.Clone(),
		FinalURL:    finalURL,
		Redirected:  redirected,
		ContentType: contentType,
		SizeBytes:   sizeBytes,
	}
}

func classifyAPIError(resp *http.Response, body []byte) *APIError {
	payload := struct {
		Message          string `json:"message"`
		DocumentationURL string `json:"documentation_url"`
	}{}
	_ = json.Unmarshal(body, &payload)

	message := strings.TrimSpace(payload.Message)
	if message == "" {
		message = fmt.Sprintf("GitHub API returned HTTP %d", resp.StatusCode)
	}
	lowerMessage := strings.ToLower(message)
	code := "github_api_error"
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		code = "unauthorized"
	case resp.StatusCode == http.StatusForbidden && strings.TrimSpace(resp.Header.Get("X-RateLimit-Remaining")) == "0":
		code = "rate_limited"
	case strings.Contains(lowerMessage, "artifact") && strings.Contains(lowerMessage, "expir"):
		code = "artifact_expired"
	case strings.Contains(lowerMessage, "log") && strings.Contains(lowerMessage, "expir"):
		code = "logs_expired"
	case resp.StatusCode == http.StatusForbidden:
		code = "forbidden"
	case resp.StatusCode == http.StatusNotFound:
		code = "not_found"
	case resp.StatusCode == http.StatusGone:
		code = "gone"
	case resp.StatusCode == http.StatusUnprocessableEntity:
		code = "validation_failed"
	case resp.StatusCode >= 500:
		code = "github_unavailable"
	}
	return &APIError{
		Status:           resp.StatusCode,
		Code:             code,
		Message:          message,
		DocumentationURL: strings.TrimSpace(payload.DocumentationURL),
	}
}

func writeFile(fsys sandbox.FileSystem, resolvePath func(string) (string, error), relativePath string, data []byte) error {
	if fsys == nil {
		return fmt.Errorf("file system is unavailable")
	}
	if resolvePath == nil {
		return fmt.Errorf("path resolver is unavailable")
	}
	relativePath = strings.TrimSpace(relativePath)
	if relativePath == "" {
		return fmt.Errorf("relative path must not be empty")
	}
	resolved, err := resolvePath(relativePath)
	if err != nil {
		return err
	}
	if err := fsys.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return err
	}
	if err := fsys.WriteFile(resolved, data, 0o644); err != nil {
		return err
	}
	return nil
}

func extractZipData(data []byte, extractRoot string, fsys sandbox.FileSystem, resolvePath func(string) (string, error)) ([]FileReference, error) {
	if strings.TrimSpace(extractRoot) == "" {
		return nil, fmt.Errorf("extract root must not be empty")
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open ZIP archive: %w", err)
	}
	if len(reader.File) > MaxExtractedFiles {
		return nil, fmt.Errorf("ZIP archive has %d files, exceeding the limit of %d", len(reader.File), MaxExtractedFiles)
	}

	var (
		totalExtracted int64
		files          []FileReference
	)
	for _, file := range reader.File {
		cleanName, err := safeZipPath(file.Name)
		if err != nil {
			return nil, err
		}
		targetRelPath := filepath.ToSlash(filepath.Join(extractRoot, filepath.FromSlash(cleanName)))
		if file.FileInfo().IsDir() {
			if err := mkdirAll(fsys, resolvePath, targetRelPath); err != nil {
				return nil, err
			}
			continue
		}
		if !file.FileInfo().Mode().IsRegular() {
			return nil, fmt.Errorf("ZIP archive contains unsupported file type %q", file.Name)
		}
		rc, err := file.Open()
		if err != nil {
			return nil, fmt.Errorf("open ZIP member %q: %w", file.Name, err)
		}
		remainingBytes := MaxExtractedBytes - totalExtracted
		if remainingBytes <= 0 {
			rc.Close()
			return nil, fmt.Errorf("ZIP archive exceeds the extracted size limit of %d bytes", MaxExtractedBytes)
		}
		memberBytes, err := io.ReadAll(io.LimitReader(rc, remainingBytes+1))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read ZIP member %q: %w", file.Name, err)
		}
		if int64(len(memberBytes)) > remainingBytes {
			return nil, fmt.Errorf("ZIP archive exceeds the extracted size limit of %d bytes", MaxExtractedBytes)
		}
		totalExtracted += int64(len(memberBytes))
		if totalExtracted > MaxExtractedBytes {
			return nil, fmt.Errorf("ZIP archive exceeds the extracted size limit of %d bytes", MaxExtractedBytes)
		}
		if err := writeFile(fsys, resolvePath, targetRelPath, memberBytes); err != nil {
			return nil, err
		}
		files = append(files, FileReference{
			Path:        targetRelPath,
			MimeType:    detectMimeType(targetRelPath, ""),
			Description: "Extracted artifact file",
			SizeBytes:   int64(len(memberBytes)),
		})
	}
	return files, nil
}

func mkdirAll(fsys sandbox.FileSystem, resolvePath func(string) (string, error), relativePath string) error {
	if fsys == nil {
		return fmt.Errorf("file system is unavailable")
	}
	if resolvePath == nil {
		return fmt.Errorf("path resolver is unavailable")
	}
	resolved, err := resolvePath(relativePath)
	if err != nil {
		return err
	}
	return fsys.MkdirAll(resolved, 0o755)
}

func safeZipPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("ZIP archive contains an empty path")
	}
	raw = strings.ReplaceAll(raw, "\\", "/")
	clean := path.Clean(raw)
	switch {
	case clean == "." || clean == "..":
		return "", fmt.Errorf("ZIP archive contains an invalid path %q", raw)
	case strings.HasPrefix(clean, "../"):
		return "", fmt.Errorf("ZIP archive contains an unsafe path %q", raw)
	case strings.HasPrefix(clean, "/"):
		return "", fmt.Errorf("ZIP archive contains an absolute path %q", raw)
	}
	return clean, nil
}

func detectMimeType(pathValue, headerValue string) string {
	headerValue = strings.TrimSpace(headerValue)
	if headerValue != "" {
		if semi := strings.Index(headerValue, ";"); semi >= 0 {
			headerValue = strings.TrimSpace(headerValue[:semi])
		}
		if headerValue != "" && headerValue != "application/octet-stream" {
			return headerValue
		}
	}
	extension := strings.TrimSpace(filepath.Ext(pathValue))
	if extension == "" {
		return "application/octet-stream"
	}
	if guessed := mime.TypeByExtension(extension); guessed != "" {
		if semi := strings.Index(guessed, ";"); semi >= 0 {
			return strings.TrimSpace(guessed[:semi])
		}
		return guessed
	}
	return "application/octet-stream"
}
