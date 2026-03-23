package describeimage

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
)

const tinyPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO7Zk1sAAAAASUVORK5CYII="

func TestBuildRequestFromFilePath(t *testing.T) {
	t.Parallel()

	toolCtx := newTestToolContext(t, t.TempDir())
	imageBytes := mustDecodeBase64(t, tinyPNGBase64)
	imagePath := filepath.Join(toolCtx.root, "pixel.png")
	if err := os.WriteFile(imagePath, imageBytes, 0o644); err != nil {
		t.Fatalf("os.WriteFile(pixel.png) error = %v", err)
	}

	request, err := buildRequest(toolCtx, args{
		FilePath: "pixel.png",
		Prompt:   "Summarize the image.",
	})
	if err != nil {
		t.Fatalf("buildRequest() error = %v", err)
	}
	if request.Prompt != "Summarize the image." {
		t.Fatalf("request.Prompt = %q, want custom prompt", request.Prompt)
	}
	if request.MediaType != "image/png" {
		t.Fatalf("request.MediaType = %q, want image/png", request.MediaType)
	}
	if string(request.Data) != string(imageBytes) {
		t.Fatal("request.Data did not match file contents")
	}
}

func TestBuildRequestFromBase64DataURL(t *testing.T) {
	t.Parallel()

	request, err := buildRequest(nil, args{
		ImageBase64: "data:image/png;base64," + tinyPNGBase64,
	})
	if err != nil {
		t.Fatalf("buildRequest() error = %v", err)
	}
	if request.Prompt != defaultDescribeImageText {
		t.Fatalf("request.Prompt = %q, want default prompt", request.Prompt)
	}
	if request.MediaType != "image/png" {
		t.Fatalf("request.MediaType = %q, want image/png", request.MediaType)
	}
	if len(request.Data) == 0 {
		t.Fatal("request.Data is empty, want decoded image bytes")
	}
}

func TestBuildRequestFromImageURL(t *testing.T) {
	t.Parallel()

	request, err := buildRequest(nil, args{
		ImageURL: "https://example.com/assets/pixel.png",
		Prompt:   "Describe the remote image.",
	})
	if err != nil {
		t.Fatalf("buildRequest() error = %v", err)
	}
	if request.Prompt != "Describe the remote image." {
		t.Fatalf("request.Prompt = %q, want custom prompt", request.Prompt)
	}
	if request.ImageURL != "https://example.com/assets/pixel.png" {
		t.Fatalf("request.ImageURL = %q, want normalized URL", request.ImageURL)
	}
	if request.MediaType != "" {
		t.Fatalf("request.MediaType = %q, want empty for URL-backed request", request.MediaType)
	}
	if len(request.Data) != 0 {
		t.Fatal("request.Data is non-empty, want no inline bytes for URL-backed request")
	}
}

func TestBuildRequestRejectsInvalidBase64(t *testing.T) {
	t.Parallel()

	_, err := buildRequest(nil, args{
		ImageBase64: "%%%not-base64%%%",
		MediaType:   "image/png",
	})
	if err == nil {
		t.Fatal("buildRequest() error = nil, want invalid base64 rejection")
	}
	if !strings.Contains(err.Error(), "not valid base64") {
		t.Fatalf("buildRequest() error = %v, want invalid base64 rejection", err)
	}
}

func TestBuildRequestRejectsConflictingSources(t *testing.T) {
	t.Parallel()

	_, err := buildRequest(nil, args{
		FilePath:    "/tmp/pixel.png",
		ImageBase64: tinyPNGBase64,
		ImageURL:    "https://example.com/pixel.png",
		MediaType:   "image/png",
	})
	if err == nil {
		t.Fatal("buildRequest() error = nil, want conflicting source rejection")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("buildRequest() error = %v, want conflicting source rejection", err)
	}
}

func TestBuildRequestRejectsMediaTypeMismatch(t *testing.T) {
	t.Parallel()

	_, err := buildRequest(nil, args{
		ImageBase64: tinyPNGBase64,
		MediaType:   "image/jpeg",
	})
	if err == nil {
		t.Fatal("buildRequest() error = nil, want media type mismatch")
	}
	if !strings.Contains(err.Error(), "does not match detected media type") {
		t.Fatalf("buildRequest() error = %v, want media type mismatch", err)
	}
}

func TestBuildRequestRejectsInvalidImageURL(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "relative URL",
			url:  "/assets/pixel.png",
			want: "absolute http or https URL",
		},
		{
			name: "unsupported scheme",
			url:  "ftp://example.com/pixel.png",
			want: "must use http or https",
		},
		{
			name: "localhost",
			url:  "https://localhost/pixel.png",
			want: "publicly reachable",
		},
		{
			name: "private IP",
			url:  "https://192.168.1.25/pixel.png",
			want: "private or local IP",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := buildRequest(nil, args{ImageURL: tc.url})
			if err == nil {
				t.Fatal("buildRequest() error = nil, want invalid URL rejection")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("buildRequest() error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestBuildRequestRejectsMediaTypeWithImageURL(t *testing.T) {
	t.Parallel()

	_, err := buildRequest(nil, args{
		ImageURL:  "https://example.com/pixel.png",
		MediaType: "image/png",
	})
	if err == nil {
		t.Fatal("buildRequest() error = nil, want media_type rejection for URL input")
	}
	if !strings.Contains(err.Error(), "media_type is only supported") {
		t.Fatalf("buildRequest() error = %v, want media_type rejection", err)
	}
}

func TestHandleFormatsNormalizedOutput(t *testing.T) {
	t.Parallel()

	toolCtx := newTestToolContext(t, t.TempDir())
	toolCtx.describeResponse = toolscore.ImageDescriptionResponse{
		Provider:    "openai",
		Model:       "gpt-5.4",
		Description: "A red square with the word demo.",
	}
	step := &toolscore.Step{}
	call := &toolscore.ToolAction{
		Name:  toolName,
		Kind:  toolscore.ToolKindFunction,
		Input: `{"image_base64":"` + tinyPNGBase64 + `","media_type":"image/png","prompt":"Read the visible text."}`,
	}

	if err := handle(context.Background(), toolCtx, step, call); err != nil {
		t.Fatalf("handle() error = %v", err)
	}
	if toolCtx.policyErr != nil {
		t.Fatalf("toolCtx policy error = %v, want nil", toolCtx.policyErr)
	}
	if toolCtx.describeRequest.Prompt != "Read the visible text." {
		t.Fatalf("describeRequest.Prompt = %q, want custom prompt", toolCtx.describeRequest.Prompt)
	}
	if toolCtx.describeRequest.MediaType != "image/png" {
		t.Fatalf("describeRequest.MediaType = %q, want image/png", toolCtx.describeRequest.MediaType)
	}
	if !strings.Contains(toolCtx.output, "Provider: openai") {
		t.Fatalf("output = %q, want provider line", toolCtx.output)
	}
	if !strings.Contains(toolCtx.output, "Model: gpt-5.4") {
		t.Fatalf("output = %q, want model line", toolCtx.output)
	}
	if !strings.Contains(toolCtx.output, "Description:\nA red square with the word demo.") {
		t.Fatalf("output = %q, want normalized description block", toolCtx.output)
	}
}

type testToolContext struct {
	t                *testing.T
	root             string
	fs               sandbox.FileSystem
	describeRequest  toolscore.ImageDescriptionRequest
	describeResponse toolscore.ImageDescriptionResponse
	describeErr      error
	output           string
	policyErr        error
	parseErr         error
}

func newTestToolContext(t *testing.T, root string) *testToolContext {
	t.Helper()

	fsys, err := sandbox.NewFS(root)
	if err != nil {
		t.Fatalf("sandbox.NewFS() error = %v", err)
	}
	resolvedRoot, err := fsys.Getwd()
	if err != nil {
		t.Fatalf("fsys.Getwd() error = %v", err)
	}
	return &testToolContext{
		t:    t,
		root: resolvedRoot,
		fs:   fsys,
	}
}

func (c *testToolContext) WorkingDir() string {
	return c.root
}

func (c *testToolContext) FileSystem() sandbox.FileSystem {
	return c.fs
}

func (c *testToolContext) ResolvePath(path string) (string, error) {
	resolver, ok := c.fs.(interface {
		ResolvePath(dir, path string) (string, error)
	})
	if !ok {
		return "", errors.New("filesystem does not support ResolvePath")
	}
	return resolver.ResolvePath(c.root, path)
}

func (*testToolContext) HTTPClient(toolscore.ToolHTTPClientOptions) *http.Client {
	return nil
}

func (*testToolContext) RuntimeData() any {
	return nil
}

func (*testToolContext) StepTimeout() time.Duration {
	return 0
}

func (*testToolContext) SearchWeb(context.Context, string, int) (toolscore.WebSearchResponse, error) {
	return toolscore.WebSearchResponse{}, errors.New("unexpected SearchWeb call")
}

func (c *testToolContext) DescribeImage(_ context.Context, req toolscore.ImageDescriptionRequest) (toolscore.ImageDescriptionResponse, error) {
	c.describeRequest = req
	if c.describeErr != nil {
		return toolscore.ImageDescriptionResponse{}, c.describeErr
	}
	return c.describeResponse, nil
}

func (*testToolContext) RunShell(context.Context, *toolscore.Step, toolscore.ShellExecution) error {
	return errors.New("unexpected RunShell call")
}

func (c *testToolContext) SetOutput(_ *toolscore.Step, output string) {
	c.output = output
}

func (c *testToolContext) SetPolicyError(_ *toolscore.Step, err error) {
	c.policyErr = err
}

func (c *testToolContext) SetParseError(_ *toolscore.Step, err error) {
	c.parseErr = err
}

func mustDecodeBase64(t *testing.T, raw string) []byte {
	t.Helper()

	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		t.Fatalf("base64 decode error = %v", err)
	}
	return data
}
