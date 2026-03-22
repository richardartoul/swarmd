package agent_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/richardartoul/swarmd/pkg/agent"
	"github.com/richardartoul/swarmd/pkg/sh/interp"
)

func TestHandleTriggerExecutesHTTPRequestToolWithHeaderArray(t *testing.T) {
	t.Parallel()

	type capturedRequest struct {
		authorization string
		contentType   string
	}
	requests := make(chan capturedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- capturedRequest{
			authorization: r.Header.Get("Authorization"),
			contentType:   r.Header.Get("Content-Type"),
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	a := newAgent(t, agent.Config{
		Root:                 t.TempDir(),
		NetworkDialer:        interp.OSNetworkDialer{},
		GlobalReachableHosts: []interp.HostMatcher{{Glob: "*"}},
		Driver: &scriptedDriver{
			decisions: []agent.Decision{
				tool(agent.ToolNameHTTPRequest, agent.ToolKindFunction, `{"url":"`+server.URL+`","method":"POST","headers":[{"name":"Authorization","value":"Bearer secret"},{"name":"Content-Type","value":"application/json"}],"body":"{}","follow_redirects":true}`),
				finish("done"),
			},
		},
	})

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{ID: "http-request-with-headers"})
	if err != nil {
		t.Fatalf("HandleTrigger() error = %v", err)
	}
	request := <-requests
	if request.authorization != "Bearer secret" {
		t.Fatalf("Authorization header = %q, want %q", request.authorization, "Bearer secret")
	}
	if request.contentType != "application/json" {
		t.Fatalf("Content-Type header = %q, want %q", request.contentType, "application/json")
	}
	output := result.Steps[0].ActionOutput
	if !strings.Contains(output, "Status: 200 OK") {
		t.Fatalf("step output = %q, want HTTP status", output)
	}
	if !strings.Contains(output, `{"ok":true}`) {
		t.Fatalf("step output = %q, want JSON body", output)
	}
}
