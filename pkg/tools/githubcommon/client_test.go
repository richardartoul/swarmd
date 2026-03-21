package githubcommon

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/richardartoul/swarmd/pkg/sh/interp"
)

func TestNewClientRequiresToken(t *testing.T) {
	t.Parallel()

	_, err := NewClient(ClientConfig{
		BaseURL:    "https://api.github.com",
		HTTPClient: &http.Client{},
	})
	if err == nil {
		t.Fatal("NewClient() error = nil, want token validation error")
	}
}

func TestClientJSONMapsRateLimitErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded","documentation_url":"https://docs.github.com/rest/overview/resources-in-the-rest-api#rate-limiting"}`))
	}))
	defer server.Close()

	client, err := NewClient(ClientConfig{
		Token:      "token",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = client.JSON(context.Background(), http.MethodGet, "/repos/acme/monorepo", nil, "", nil)
	if err == nil {
		t.Fatal("client.JSON() error = nil, want API error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("client.JSON() error = %T, want *APIError", err)
	}
	if apiErr.Code != "rate_limited" {
		t.Fatalf("apiErr.Code = %q, want %q", apiErr.Code, "rate_limited")
	}
}

func TestClientDownloadWritesAndExtractsZip(t *testing.T) {
	t.Parallel()

	zipBytes := mustZIP(t, map[string]string{
		"junit.xml": "<testsuite/>",
	})
	downloadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipBytes)
	}))
	defer downloadServer.Close()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/monorepo/actions/artifacts/701/zip" {
			t.Fatalf("request path = %q, want artifact download path", r.URL.Path)
		}
		http.Redirect(w, r, downloadServer.URL+"/artifact.zip", http.StatusFound)
	}))
	defer apiServer.Close()

	root := t.TempDir()
	client, err := NewClient(ClientConfig{
		Token:      "token",
		BaseURL:    apiServer.URL,
		HTTPClient: apiServer.Client(),
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	result, err := client.Download(
		context.Background(),
		"/repos/acme/monorepo/actions/artifacts/701/zip",
		nil,
		"application/octet-stream",
		"github/actions/artifacts/701.zip",
		"Raw artifact archive",
		interp.OSFileSystem{},
		func(path string) (string, error) { return filepath.Join(root, path), nil },
		true,
		"github/actions/artifacts/701",
	)
	if err != nil {
		t.Fatalf("client.Download() error = %v", err)
	}
	if !result.Meta.Redirected {
		t.Fatal("result.Meta.Redirected = false, want true")
	}
	if len(result.Files) != 2 {
		t.Fatalf("len(result.Files) = %d, want 2", len(result.Files))
	}

	archivePath := filepath.Join(root, "github/actions/artifacts/701.zip")
	if _, err := os.Stat(archivePath); err != nil {
		t.Fatalf("os.Stat(archivePath) error = %v", err)
	}
	extractedPath := filepath.Join(root, "github/actions/artifacts/701/junit.xml")
	extractedBytes, err := os.ReadFile(extractedPath)
	if err != nil {
		t.Fatalf("os.ReadFile(extractedPath) error = %v", err)
	}
	if string(extractedBytes) != "<testsuite/>" {
		t.Fatalf("extracted file = %q, want junit payload", string(extractedBytes))
	}
}

func TestClientDownloadRejectsUnsafeZipPaths(t *testing.T) {
	t.Parallel()

	zipBytes := mustZIP(t, map[string]string{
		"..\\escape.txt": "nope",
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipBytes)
	}))
	defer server.Close()

	root := t.TempDir()
	client, err := NewClient(ClientConfig{
		Token:      "token",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = client.Download(
		context.Background(),
		"/repos/acme/monorepo/actions/artifacts/701/zip",
		nil,
		"application/octet-stream",
		"github/actions/artifacts/701.zip",
		"Raw artifact archive",
		interp.OSFileSystem{},
		func(path string) (string, error) { return filepath.Join(root, path), nil },
		true,
		"github/actions/artifacts/701",
	)
	if err == nil {
		t.Fatal("client.Download() error = nil, want unsafe ZIP path error")
	}
	if !strings.Contains(err.Error(), "unsafe path") && !strings.Contains(err.Error(), "invalid path") {
		t.Fatalf("client.Download() error = %v, want ZIP path safety error", err)
	}
}

func mustZIP(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, contents := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("writer.Create(%q) error = %v", name, err)
		}
		if _, err := entry.Write([]byte(contents)); err != nil {
			t.Fatalf("entry.Write(%q) error = %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close() error = %v", err)
	}
	return buffer.Bytes()
}
