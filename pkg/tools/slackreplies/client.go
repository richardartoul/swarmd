package slackreplies

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const defaultSlackAPIBaseURL = "https://slack.com/api"

type SlackClientConfig struct {
	Token   string
	BaseURL string
	Client  *http.Client
}

type SlackClient struct {
	token   string
	baseURL string
	client  *http.Client
}

type SlackListThreadRepliesParams struct {
	Channel  string
	ThreadTS string
	AfterTS  string
}

type SlackThreadReply struct {
	TS       string `json:"ts"`
	ThreadTS string `json:"thread_ts,omitempty"`
	User     string `json:"user,omitempty"`
	BotID    string `json:"bot_id,omitempty"`
	Text     string `json:"text,omitempty"`
	Subtype  string `json:"subtype,omitempty"`
}

type SlackListThreadRepliesResult struct {
	Channel  string             `json:"channel"`
	ThreadTS string             `json:"thread_ts"`
	Messages []SlackThreadReply `json:"messages"`
}

type slackAPIResponseEnvelope struct {
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
	Needed   string `json:"needed,omitempty"`
	Provided string `json:"provided,omitempty"`
}

type slackRepliesResponse struct {
	slackAPIResponseEnvelope
	HasMore  bool `json:"has_more"`
	Messages []struct {
		TS       string `json:"ts"`
		ThreadTS string `json:"thread_ts,omitempty"`
		User     string `json:"user,omitempty"`
		BotID    string `json:"bot_id,omitempty"`
		Text     string `json:"text,omitempty"`
		Subtype  string `json:"subtype,omitempty"`
	} `json:"messages"`
	ResponseMetadata struct {
		NextCursor string `json:"next_cursor,omitempty"`
	} `json:"response_metadata,omitempty"`
}

func NewSlackClient(cfg SlackClientConfig) (*SlackClient, error) {
	token := strings.TrimSpace(cfg.Token)
	if token == "" {
		return nil, fmt.Errorf("slack client requires %s", SlackUserTokenEnvVar)
	}
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		baseURL = defaultSlackAPIBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	client := cfg.Client
	if client == nil {
		client = http.DefaultClient
	}
	return &SlackClient{
		token:   token,
		baseURL: baseURL,
		client:  client,
	}, nil
}

func (c *SlackClient) ListThreadReplies(ctx context.Context, params SlackListThreadRepliesParams) (SlackListThreadRepliesResult, error) {
	channel := strings.TrimSpace(params.Channel)
	threadTS := strings.TrimSpace(params.ThreadTS)
	afterTS := strings.TrimSpace(params.AfterTS)
	query := url.Values{
		"channel": {channel},
		"ts":      {threadTS},
		"limit":   {"200"},
	}
	if afterTS != "" {
		query.Set("oldest", afterTS)
		query.Set("inclusive", "false")
	}

	messages := make([]SlackThreadReply, 0)
	cursor := ""
	for {
		pageQuery := cloneURLValues(query)
		if cursor != "" {
			pageQuery.Set("cursor", cursor)
		}

		var response slackRepliesResponse
		if err := c.getJSON(ctx, "/conversations.replies", pageQuery, &response); err != nil {
			return SlackListThreadRepliesResult{}, err
		}
		for _, message := range response.Messages {
			if strings.TrimSpace(message.TS) == threadTS {
				continue
			}
			if afterTS != "" && compareSlackTimestamp(message.TS, afterTS) <= 0 {
				continue
			}
			messages = append(messages, SlackThreadReply{
				TS:       strings.TrimSpace(message.TS),
				ThreadTS: strings.TrimSpace(message.ThreadTS),
				User:     strings.TrimSpace(message.User),
				BotID:    strings.TrimSpace(message.BotID),
				Text:     message.Text,
				Subtype:  strings.TrimSpace(message.Subtype),
			})
		}
		cursor = strings.TrimSpace(response.ResponseMetadata.NextCursor)
		if cursor == "" {
			break
		}
	}

	return SlackListThreadRepliesResult{
		Channel:  channel,
		ThreadTS: threadTS,
		Messages: messages,
	}, nil
}

func (c *SlackClient) getJSON(ctx context.Context, path string, query url.Values, dst any) error {
	endpoint := c.baseURL + path
	if encodedQuery := query.Encode(); encodedQuery != "" {
		endpoint += "?" + encodedQuery
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build slack request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("perform slack request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read slack response: %w", err)
	}

	var apiEnvelope slackAPIResponseEnvelope
	if err := json.Unmarshal(respBody, &apiEnvelope); err == nil && !apiEnvelope.OK {
		return fmt.Errorf("slack api error (%s): %s", resp.Status, formatSlackAPIError(apiEnvelope))
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

func formatSlackAPIError(response slackAPIResponseEnvelope) string {
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

func cloneURLValues(values url.Values) url.Values {
	if len(values) == 0 {
		return nil
	}
	cloned := make(url.Values, len(values))
	for key, parts := range values {
		cloned[key] = append([]string(nil), parts...)
	}
	return cloned
}

func compareSlackTimestamp(a, b string) int {
	aSeconds, aFraction := splitSlackTimestamp(a)
	bSeconds, bFraction := splitSlackTimestamp(b)
	if cmp := compareNumericStrings(aSeconds, bSeconds); cmp != 0 {
		return cmp
	}
	return compareNumericStrings(aFraction, bFraction)
}

func splitSlackTimestamp(value string) (string, string) {
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
