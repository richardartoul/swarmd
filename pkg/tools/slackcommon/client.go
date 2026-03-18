package slackcommon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	SlackUserTokenEnvVar   = "SLACK_USER_TOKEN"
	SlackAPIBaseURLEnvVar  = "SLACK_API_BASE_URL"
	DefaultSlackAPIBaseURL = "https://slack.com/api"
)

type ClientConfig struct {
	Token   string
	BaseURL string
	Client  *http.Client
}

type Client struct {
	token   string
	baseURL string
	client  *http.Client
}

type APIResponseEnvelope struct {
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
	Needed   string `json:"needed,omitempty"`
	Provided string `json:"provided,omitempty"`
}

func NewClient(cfg ClientConfig) (*Client, error) {
	token := strings.TrimSpace(cfg.Token)
	if token == "" {
		return nil, fmt.Errorf("slack client requires %s", SlackUserTokenEnvVar)
	}
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		baseURL = DefaultSlackAPIBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	client := cfg.Client
	if client == nil {
		client = http.DefaultClient
	}
	return &Client{
		token:   token,
		baseURL: baseURL,
		client:  client,
	}, nil
}

func (c *Client) GetJSON(ctx context.Context, path string, query url.Values, dst any) error {
	return c.doJSON(ctx, http.MethodGet, path, query, nil, dst)
}

func (c *Client) PostJSON(ctx context.Context, path string, payload any, dst any) error {
	return c.doJSON(ctx, http.MethodPost, path, nil, payload, dst)
}

func (c *Client) doJSON(ctx context.Context, method, path string, query url.Values, payload any, dst any) error {
	endpoint := c.baseURL + path
	if encodedQuery := query.Encode(); encodedQuery != "" {
		endpoint += "?" + encodedQuery
	}

	var body io.Reader
	if payload != nil {
		encodedPayload, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal slack request body: %w", err)
		}
		body = bytes.NewReader(encodedPayload)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("build slack request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("perform slack request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read slack response: %w", err)
	}

	var apiEnvelope APIResponseEnvelope
	if err := json.Unmarshal(respBody, &apiEnvelope); err == nil && !apiEnvelope.OK {
		return fmt.Errorf("slack api error (%s): %s", resp.Status, FormatAPIError(apiEnvelope))
	}
	if resp.StatusCode/100 != 2 {
		bodyText := strings.TrimSpace(string(respBody))
		if bodyText == "" {
			bodyText = http.StatusText(resp.StatusCode)
		}
		return fmt.Errorf("slack api error (%s): %s", resp.Status, bodyText)
	}
	if dst == nil {
		return nil
	}
	if err := json.Unmarshal(respBody, dst); err != nil {
		return fmt.Errorf("decode slack response: %w", err)
	}
	return nil
}

func FormatAPIError(response APIResponseEnvelope) string {
	message := strings.TrimSpace(response.Error)
	if message == "" {
		message = "unknown_error"
	}
	if strings.TrimSpace(response.Needed) == "" {
		return message
	}
	if strings.TrimSpace(response.Provided) == "" {
		return fmt.Sprintf("%s (needed=%s)", message, response.Needed)
	}
	return fmt.Sprintf("%s (needed=%s provided=%s)", message, response.Needed, response.Provided)
}

func CloneURLValues(values url.Values) url.Values {
	if len(values) == 0 {
		return nil
	}
	cloned := make(url.Values, len(values))
	for key, parts := range values {
		cloned[key] = append([]string(nil), parts...)
	}
	return cloned
}

func CompareTimestamp(a, b string) int {
	aSeconds, aFraction := splitTimestamp(a)
	bSeconds, bFraction := splitTimestamp(b)
	if cmp := compareNumericStrings(aSeconds, bSeconds); cmp != 0 {
		return cmp
	}
	return compareNumericStrings(aFraction, bFraction)
}

func splitTimestamp(value string) (string, string) {
	seconds, fraction, ok := strings.Cut(strings.TrimSpace(value), ".")
	if !ok {
		return seconds, ""
	}
	return seconds, fraction
}

func compareNumericStrings(a, b string) int {
	a = trimLeadingZeroes(a)
	b = trimLeadingZeroes(b)
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func trimLeadingZeroes(value string) string {
	value = strings.TrimLeft(strings.TrimSpace(value), "0")
	if value == "" {
		return "0"
	}
	return value
}
