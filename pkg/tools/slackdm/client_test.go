package slackdm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSlackClientLookupUserByEmail(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users.lookupByEmail" {
			t.Fatalf("request path = %q, want %q", r.URL.Path, "/users.lookupByEmail")
		}
		if r.Method != http.MethodGet {
			t.Fatalf("request method = %q, want %q", r.Method, http.MethodGet)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer xoxp-test" {
			t.Fatalf("Authorization header = %q, want bearer token", got)
		}
		if got := r.URL.Query().Get("email"); got != "ada@example.com" {
			t.Fatalf("query email = %q, want %q", got, "ada@example.com")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"user": map[string]any{
				"id": "U123",
				"profile": map[string]any{
					"email": "ada@example.com",
				},
			},
		})
	}))
	defer server.Close()

	client := newTestSlackClient(t, server)
	result, err := client.LookupUserByEmail(context.Background(), " Ada@Example.com ")
	if err != nil {
		t.Fatalf("LookupUserByEmail() error = %v", err)
	}
	if result.UserID != "U123" || result.Email != "ada@example.com" {
		t.Fatalf("LookupUserByEmail() result = %#v, want normalized email and user id", result)
	}
}

func TestSlackClientOpenDM(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/conversations.open" {
			t.Fatalf("request path = %q, want %q", r.URL.Path, "/conversations.open")
		}
		if r.Method != http.MethodPost {
			t.Fatalf("request method = %q, want %q", r.Method, http.MethodPost)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		if got := body["users"]; got != "U123" {
			t.Fatalf("request users = %#v, want %q", got, "U123")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"channel": map[string]any{
				"id": "D123",
			},
		})
	}))
	defer server.Close()

	client := newTestSlackClient(t, server)
	result, err := client.OpenDM(context.Background(), "U123")
	if err != nil {
		t.Fatalf("OpenDM() error = %v", err)
	}
	if result.Channel != "D123" {
		t.Fatalf("OpenDM() result = %#v, want channel %q", result, "D123")
	}
}

func TestSlackClientPostMessage(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.postMessage" {
			t.Fatalf("request path = %q, want %q", r.URL.Path, "/chat.postMessage")
		}
		if r.Method != http.MethodPost {
			t.Fatalf("request method = %q, want %q", r.Method, http.MethodPost)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		if got := body["channel"]; got != "D123" {
			t.Fatalf("request channel = %#v, want %q", got, "D123")
		}
		if got := body["text"]; got != "hello from test" {
			t.Fatalf("request text = %#v, want %q", got, "hello from test")
		}
		if _, ok := body["thread_ts"]; ok {
			t.Fatalf("request body unexpectedly included thread_ts: %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"channel": "D123",
			"ts":      "1700.000002",
			"message": map[string]any{
				"ts": "1700.000002",
			},
		})
	}))
	defer server.Close()

	client := newTestSlackClient(t, server)
	result, err := client.PostMessage(context.Background(), SlackPostMessageParams{
		Channel: "D123",
		Text:    "hello from test",
	})
	if err != nil {
		t.Fatalf("PostMessage() error = %v", err)
	}
	if result.Channel != "D123" || result.TS != "1700.000002" || result.ThreadTS != "1700.000002" {
		t.Fatalf("PostMessage() result = %#v, want channel/ts/thread_ts", result)
	}
}

func TestSlackClientFormatsAPIErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":       false,
			"error":    "missing_scope",
			"needed":   "users:read.email",
			"provided": "chat:write",
		})
	}))
	defer server.Close()

	client := newTestSlackClient(t, server)
	_, err := client.LookupUserByEmail(context.Background(), "ada@example.com")
	if err == nil {
		t.Fatal("LookupUserByEmail() error = nil, want API error")
	}
	if !strings.Contains(err.Error(), "missing_scope") || !strings.Contains(err.Error(), "users:read.email") {
		t.Fatalf("LookupUserByEmail() error = %v, want formatted slack scope error", err)
	}
}

func newTestSlackClient(t *testing.T, server *httptest.Server) *SlackClient {
	t.Helper()

	client, err := NewSlackClient(SlackClientConfig{
		Token:   "xoxp-test",
		BaseURL: server.URL,
		Client:  server.Client(),
	})
	if err != nil {
		t.Fatalf("NewSlackClient() error = %v", err)
	}
	return client
}
