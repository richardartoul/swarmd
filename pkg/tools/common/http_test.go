package common

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
)

// bodyCloseTrackingTransport counts response bodies that have been handed out
// but not yet closed, so tests can prove DoToolHTTPRequest never leaks one.
type bodyCloseTrackingTransport struct {
	next       http.RoundTripper
	openBodies atomic.Int64
}

func (t *bodyCloseTrackingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	t.openBodies.Add(1)
	resp.Body = &closeTrackingBody{ReadCloser: resp.Body, openBodies: &t.openBodies}
	return resp, nil
}

type closeTrackingBody struct {
	io.ReadCloser
	openBodies *atomic.Int64
	closed     atomic.Bool
}

func (b *closeTrackingBody) Close() error {
	if b.closed.CompareAndSwap(false, true) {
		b.openBodies.Add(-1)
	}
	return b.ReadCloser.Close()
}

// httpToolContext is a minimal toolscore.ToolContext for exercising
// DoToolHTTPRequest. Only HTTPClient is meaningful.
type httpToolContext struct {
	toolscore.UnimplementedToolContext
	transport http.RoundTripper
}

func (c httpToolContext) HTTPClient(opts toolscore.ToolHTTPClientOptions) *http.Client {
	client := &http.Client{Transport: c.transport}
	if !opts.FollowRedirects {
		client.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}
	return client
}

func TestDoToolHTTPRequestClosesBodyOnSuccess(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello tool")
	}))
	defer server.Close()

	transport := &bodyCloseTrackingTransport{next: http.DefaultTransport}
	resp, err := DoToolHTTPRequest(context.Background(), httpToolContext{transport: transport}, ToolHTTPRequest{
		URL: server.URL,
	})
	if err != nil {
		t.Fatalf("DoToolHTTPRequest() error = %v", err)
	}
	if string(resp.Body) != "hello tool" {
		t.Fatalf("resp.Body = %q, want %q", resp.Body, "hello tool")
	}
	if resp.BodyTruncated {
		t.Fatal("resp.BodyTruncated = true, want false")
	}
	if open := transport.openBodies.Load(); open != 0 {
		t.Fatalf("open response bodies = %d, want 0", open)
	}
}

func TestDoToolHTTPRequestClosesBodyOnTruncation(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, strings.Repeat("x", 1024))
	}))
	defer server.Close()

	transport := &bodyCloseTrackingTransport{next: http.DefaultTransport}
	resp, err := DoToolHTTPRequest(context.Background(), httpToolContext{transport: transport}, ToolHTTPRequest{
		URL:          server.URL,
		MaxBodyBytes: 16,
	})
	if err != nil {
		t.Fatalf("DoToolHTTPRequest() error = %v", err)
	}
	if !resp.BodyTruncated {
		t.Fatal("resp.BodyTruncated = false, want true")
	}
	if len(resp.Body) != 16 {
		t.Fatalf("len(resp.Body) = %d, want 16", len(resp.Body))
	}
	if open := transport.openBodies.Load(); open != 0 {
		t.Fatalf("open response bodies = %d, want 0", open)
	}
}

// TestDoToolHTTPRequestClosesBodyOnReadError pins the fix for a connection
// leak: when reading the response body failed, the response used to be
// returned to the caller unclosed, before the caller's deferred Close was
// ever registered.
func TestDoToolHTTPRequestClosesBodyOnReadError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Advertise more bytes than are sent so the client read fails.
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write([]byte("short"))
	}))
	defer server.Close()

	transport := &bodyCloseTrackingTransport{next: http.DefaultTransport}
	_, err := DoToolHTTPRequest(context.Background(), httpToolContext{transport: transport}, ToolHTTPRequest{
		URL: server.URL,
	})
	if err == nil {
		t.Fatal("DoToolHTTPRequest() error = nil, want body read failure")
	}
	if open := transport.openBodies.Load(); open != 0 {
		t.Fatalf("open response bodies = %d, want 0 (body leaked on read error)", open)
	}
}

func TestDoToolHTTPRequestHonorsRedirectLimit(t *testing.T) {
	t.Parallel()

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, server.URL+r.URL.Path+"x", http.StatusFound)
	}))
	defer server.Close()

	transport := &bodyCloseTrackingTransport{next: http.DefaultTransport}
	_, err := DoToolHTTPRequest(context.Background(), httpToolContext{transport: transport}, ToolHTTPRequest{
		URL:             server.URL,
		FollowRedirects: true,
	})
	if err == nil || !strings.Contains(err.Error(), "redirects") {
		t.Fatalf("DoToolHTTPRequest() error = %v, want redirect limit failure", err)
	}
	if open := transport.openBodies.Load(); open != 0 {
		t.Fatalf("open response bodies = %d, want 0", open)
	}
}
