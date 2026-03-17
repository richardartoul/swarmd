package slackpost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

type SlackPostMessageParams struct {
	Channel  string
	Text     string
	ThreadTS string
}

type SlackPostMessageResult struct {
	Channel  string `json:"channel"`
	TS       string `json:"ts"`
	ThreadTS string `json:"thread_ts"`
}

type slackAPIResponseEnvelope struct {
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
	Needed   string `json:"needed,omitempty"`
	Provided string `json:"provided,omitempty"`
}

type slackPostMessageResponse struct {
	slackAPIResponseEnvelope
	Channel string `json:"channel"`
	TS      string `json:"ts"`
	Message struct {
		TS       string `json:"ts"`
		ThreadTS string `json:"thread_ts,omitempty"`
	} `json:"message"`
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

func (c *SlackClient) PostMessage(ctx context.Context, params SlackPostMessageParams) (SlackPostMessageResult, error) {
	payload := struct {
		Channel  string `json:"channel"`
		Text     string `json:"text"`
		ThreadTS string `json:"thread_ts,omitempty"`
	}{
		Channel:  strings.TrimSpace(params.Channel),
		Text:     strings.TrimSpace(params.Text),
		ThreadTS: strings.TrimSpace(params.ThreadTS),
	}
	var response slackPostMessageResponse
	if err := c.postJSON(ctx, "/chat.postMessage", payload, &response); err != nil {
		return SlackPostMessageResult{}, err
	}
	threadTS := strings.TrimSpace(response.Message.ThreadTS)
	if threadTS == "" {
		threadTS = strings.TrimSpace(response.TS)
	}
	result := SlackPostMessageResult{
		Channel:  strings.TrimSpace(response.Channel),
		TS:       strings.TrimSpace(response.TS),
		ThreadTS: threadTS,
	}
	if result.Channel == "" || result.TS == "" || result.ThreadTS == "" {
		return SlackPostMessageResult{}, fmt.Errorf("slack postMessage response missing channel/ts/thread_ts")
	}
	return result, nil
}

func (c *SlackClient) postJSON(ctx context.Context, path string, payload any, dst any) error {
	return c.doJSON(ctx, http.MethodPost, path, payload, dst)
}

func (c *SlackClient) doJSON(ctx context.Context, method, path string, payload any, dst any) error {
	var body io.Reader
	if payload != nil {
		encodedPayload, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal slack request body: %w", err)
		}
		body = bytes.NewReader(encodedPayload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
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
