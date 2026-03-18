package slackpost

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/richardartoul/swarmd/pkg/tools/slackcommon"
)

type SlackClientConfig struct {
	Token   string
	BaseURL string
	Client  *http.Client
}

type SlackClient struct {
	client *slackcommon.Client
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

type slackPostMessageResponse struct {
	slackcommon.APIResponseEnvelope
	Channel string `json:"channel"`
	TS      string `json:"ts"`
	Message struct {
		TS       string `json:"ts"`
		ThreadTS string `json:"thread_ts,omitempty"`
	} `json:"message"`
}

func NewSlackClient(cfg SlackClientConfig) (*SlackClient, error) {
	client, err := slackcommon.NewClient(slackcommon.ClientConfig{
		Token:   cfg.Token,
		BaseURL: cfg.BaseURL,
		Client:  cfg.Client,
	})
	if err != nil {
		return nil, err
	}
	return &SlackClient{
		client: client,
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
	if err := c.client.PostJSON(ctx, "/chat.postMessage", payload, &response); err != nil {
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
