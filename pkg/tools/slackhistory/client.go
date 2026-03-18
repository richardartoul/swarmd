package slackhistory

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/richardartoul/swarmd/pkg/tools/slackcommon"
)

const defaultHistoryPageSize = 15

type SlackClientConfig struct {
	Token   string
	BaseURL string
	Client  *http.Client
}

type SlackClient struct {
	client *slackcommon.Client
}

type SlackListChannelHistoryParams struct {
	Channel     string
	AfterTS     string
	BeforeTS    string
	MaxMessages int
}

type SlackChannelMessage struct {
	TS          string   `json:"ts"`
	ThreadTS    string   `json:"thread_ts,omitempty"`
	Type        string   `json:"type,omitempty"`
	User        string   `json:"user,omitempty"`
	BotID       string   `json:"bot_id,omitempty"`
	Text        string   `json:"text,omitempty"`
	Subtype     string   `json:"subtype,omitempty"`
	ReplyCount  int      `json:"reply_count,omitempty"`
	LatestReply string   `json:"latest_reply,omitempty"`
	ReplyUsers  []string `json:"reply_users,omitempty"`
}

type SlackListChannelHistoryResult struct {
	Channel       string                `json:"channel"`
	AfterTS       string                `json:"after_ts"`
	BeforeTS      string                `json:"before_ts,omitempty"`
	Messages      []SlackChannelMessage `json:"messages"`
	ReturnedCount int                   `json:"returned_count"`
	Truncated     bool                  `json:"truncated"`
	NextCursor    string                `json:"next_cursor,omitempty"`
}

type slackHistoryResponse struct {
	slackcommon.APIResponseEnvelope
	HasMore  bool `json:"has_more"`
	Messages []struct {
		TS          string   `json:"ts"`
		ThreadTS    string   `json:"thread_ts,omitempty"`
		Type        string   `json:"type,omitempty"`
		User        string   `json:"user,omitempty"`
		BotID       string   `json:"bot_id,omitempty"`
		Text        string   `json:"text,omitempty"`
		Subtype     string   `json:"subtype,omitempty"`
		ReplyCount  int      `json:"reply_count,omitempty"`
		LatestReply string   `json:"latest_reply,omitempty"`
		ReplyUsers  []string `json:"reply_users,omitempty"`
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
	return &SlackClient{client: client}, nil
}

func (c *SlackClient) ListChannelHistory(ctx context.Context, params SlackListChannelHistoryParams) (SlackListChannelHistoryResult, error) {
	channel := strings.TrimSpace(params.Channel)
	afterTS := strings.TrimSpace(params.AfterTS)
	beforeTS := strings.TrimSpace(params.BeforeTS)

	baseQuery := url.Values{
		"channel": {channel},
	}
	if afterTS != "" {
		baseQuery.Set("oldest", afterTS)
	}
	if beforeTS != "" {
		baseQuery.Set("latest", beforeTS)
	}
	if afterTS != "" || beforeTS != "" {
		baseQuery.Set("inclusive", "false")
	}

	messages := make([]SlackChannelMessage, 0)
	cursor := ""
	truncated := false
	nextCursor := ""

	for {
		pageQuery := slackcommon.CloneURLValues(baseQuery)
		limit := defaultHistoryPageSize
		if params.MaxMessages > 0 {
			remaining := params.MaxMessages - len(messages)
			if remaining <= 0 {
				truncated = cursor != ""
				nextCursor = strings.TrimSpace(cursor)
				break
			}
			if remaining < limit {
				limit = remaining
			}
		}
		pageQuery.Set("limit", strconv.Itoa(limit))
		if cursor != "" {
			pageQuery.Set("cursor", cursor)
		}

		var response slackHistoryResponse
		if err := c.client.GetJSON(ctx, "/conversations.history", pageQuery, &response); err != nil {
			return SlackListChannelHistoryResult{}, err
		}
		for _, message := range response.Messages {
			ts := strings.TrimSpace(message.TS)
			if ts == "" {
				continue
			}
			if afterTS != "" && slackcommon.CompareTimestamp(ts, afterTS) <= 0 {
				continue
			}
			if beforeTS != "" && slackcommon.CompareTimestamp(ts, beforeTS) >= 0 {
				continue
			}
			messages = append(messages, SlackChannelMessage{
				TS:          ts,
				ThreadTS:    strings.TrimSpace(message.ThreadTS),
				Type:        strings.TrimSpace(message.Type),
				User:        strings.TrimSpace(message.User),
				BotID:       strings.TrimSpace(message.BotID),
				Text:        message.Text,
				Subtype:     strings.TrimSpace(message.Subtype),
				ReplyCount:  message.ReplyCount,
				LatestReply: strings.TrimSpace(message.LatestReply),
				ReplyUsers:  normalizeReplyUsers(message.ReplyUsers),
			})
		}

		cursor = strings.TrimSpace(response.ResponseMetadata.NextCursor)
		if cursor == "" {
			break
		}
		if params.MaxMessages > 0 && len(messages) >= params.MaxMessages {
			truncated = true
			nextCursor = cursor
			break
		}
	}

	reverseMessages(messages)
	return SlackListChannelHistoryResult{
		Channel:       channel,
		AfterTS:       afterTS,
		BeforeTS:      beforeTS,
		Messages:      messages,
		ReturnedCount: len(messages),
		Truncated:     truncated,
		NextCursor:    nextCursor,
	}, nil
}

func normalizeReplyUsers(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		normalized = append(normalized, trimmed)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func reverseMessages(messages []SlackChannelMessage) {
	for left, right := 0, len(messages)-1; left < right; left, right = left+1, right-1 {
		messages[left], messages[right] = messages[right], messages[left]
	}
}
