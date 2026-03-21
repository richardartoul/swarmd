package slackdm

import (
	"context"
	"fmt"
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

type SlackLookupUserByEmailResult struct {
	UserID string `json:"user_id"`
	Email  string `json:"email,omitempty"`
}

type SlackOpenDMResult struct {
	Channel string `json:"channel"`
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

type slackLookupUserByEmailResponse struct {
	slackcommon.APIResponseEnvelope
	User struct {
		ID      string `json:"id"`
		Profile struct {
			Email string `json:"email,omitempty"`
		} `json:"profile,omitempty"`
	} `json:"user"`
}

type slackOpenDMResponse struct {
	slackcommon.APIResponseEnvelope
	Channel struct {
		ID string `json:"id"`
	} `json:"channel"`
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

func (c *SlackClient) LookupUserByEmail(ctx context.Context, email string) (SlackLookupUserByEmailResult, error) {
	normalizedEmail := normalizeEmail(email)
	query := url.Values{
		"email": {normalizedEmail},
	}
	var response slackLookupUserByEmailResponse
	if err := c.client.GetJSON(ctx, "/users.lookupByEmail", query, &response); err != nil {
		return SlackLookupUserByEmailResult{}, err
	}
	userID := strings.TrimSpace(response.User.ID)
	if userID == "" {
		return SlackLookupUserByEmailResult{}, fmt.Errorf("slack users.lookupByEmail response missing user.id")
	}
	resultEmail := normalizeEmail(response.User.Profile.Email)
	if resultEmail == "" {
		resultEmail = normalizedEmail
	}
	return SlackLookupUserByEmailResult{
		UserID: userID,
		Email:  resultEmail,
	}, nil
}

func (c *SlackClient) OpenDM(ctx context.Context, userID string) (SlackOpenDMResult, error) {
	payload := struct {
		Users string `json:"users"`
	}{
		Users: strings.TrimSpace(userID),
	}
	var response slackOpenDMResponse
	if err := c.client.PostJSON(ctx, "/conversations.open", payload, &response); err != nil {
		return SlackOpenDMResult{}, err
	}
	channel := strings.TrimSpace(response.Channel.ID)
	if channel == "" {
		return SlackOpenDMResult{}, fmt.Errorf("slack conversations.open response missing channel.id")
	}
	return SlackOpenDMResult{Channel: channel}, nil
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
