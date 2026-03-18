package slackreplies

import (
	"context"
	"net/http"
	"net/url"
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

type slackRepliesResponse struct {
	slackcommon.APIResponseEnvelope
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
		pageQuery := slackcommon.CloneURLValues(query)
		if cursor != "" {
			pageQuery.Set("cursor", cursor)
		}

		var response slackRepliesResponse
		if err := c.client.GetJSON(ctx, "/conversations.replies", pageQuery, &response); err != nil {
			return SlackListThreadRepliesResult{}, err
		}
		for _, message := range response.Messages {
			if strings.TrimSpace(message.TS) == threadTS {
				continue
			}
			if afterTS != "" && slackcommon.CompareTimestamp(message.TS, afterTS) <= 0 {
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
