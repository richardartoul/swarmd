package common

import (
	"encoding/json"
	"strings"
	"testing"

	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
)

func TestCompactStructuredJSONOutputPreservesKnownEnvelope(t *testing.T) {
	t.Parallel()

	items := make([]map[string]any, 0, 24)
	for idx := 1; idx <= 24; idx++ {
		items = append(items, map[string]any{
			"id":      idx,
			"name":    "job",
			"details": strings.Repeat("payload-", 48),
		})
	}
	raw, err := MarshalToolOutput(map[string]any{
		"tool":     "github_read_ci",
		"action":   "list_jobs",
		"ok":       true,
		"warnings": []string{},
		"page_info": map[string]any{
			"page":          1,
			"per_page":      24,
			"has_next_page": false,
		},
		"files": []toolscore.FileReference{{
			Path:        "github/actions/artifacts/701.zip",
			MimeType:    "application/zip",
			Description: "Raw artifact archive",
			SizeBytes:   83412,
		}},
		"data": map[string]any{
			"items":       items,
			"items_count": len(items),
		},
	})
	if err != nil {
		t.Fatalf("MarshalToolOutput() error = %v", err)
	}

	result, ok, err := CompactStructuredJSONOutput(raw, JSONCompactOptions{
		Budget: DefaultJSONStubBytes,
		FullOutputFile: &toolscore.FileReference{
			Path:        "/workspace/.tmp/tool-outputs/run-1/step_1/full_output.json",
			MimeType:    "application/json",
			Description: "Full structured tool output",
			SizeBytes:   int64(len(raw)),
		},
	})
	if err != nil {
		t.Fatalf("CompactStructuredJSONOutput() error = %v", err)
	}
	if !ok {
		t.Fatal("CompactStructuredJSONOutput() ok = false, want true")
	}
	if !result.Compacted {
		t.Fatal("result.Compacted = false, want true")
	}
	if !json.Valid([]byte(result.Output)) {
		t.Fatalf("result.Output = %q, want valid JSON", result.Output)
	}
	if !strings.Contains(result.Output, `"tool":"github_read_ci"`) {
		t.Fatalf("result.Output = %q, want preserved tool field", result.Output)
	}
	if !strings.Contains(result.Output, `"files"`) || !strings.Contains(result.Output, `full_output.json`) {
		t.Fatalf("result.Output = %q, want file references including full_output.json", result.Output)
	}
	if !strings.Contains(result.Output, `"kind":"json_stub_v1"`) {
		t.Fatalf("result.Output = %q, want stub metadata", result.Output)
	}
	if strings.Count(result.Output, `"id":`) > jsonStubPreviewItems {
		t.Fatalf("result.Output = %q, want bounded item preview", result.Output)
	}
}

func TestCompactStructuredJSONOutputSummarizesGenericObjects(t *testing.T) {
	t.Parallel()

	items := make([]map[string]any, 0, 12)
	for idx := 1; idx <= 12; idx++ {
		items = append(items, map[string]any{
			"id":      idx,
			"message": strings.Repeat("entry-", 32),
		})
	}
	raw, err := MarshalToolOutput(map[string]any{
		"status": "ok",
		"items":  items,
	})
	if err != nil {
		t.Fatalf("MarshalToolOutput() error = %v", err)
	}

	result, ok, err := CompactStructuredJSONOutput(raw, JSONCompactOptions{
		Budget: DefaultJSONStubBytes,
		FullOutputFile: &toolscore.FileReference{
			Path:      "/workspace/.tmp/tool-outputs/run-1/step_2/full_output.json",
			MimeType:  "application/json",
			SizeBytes: int64(len(raw)),
		},
	})
	if err != nil {
		t.Fatalf("CompactStructuredJSONOutput() error = %v", err)
	}
	if !ok {
		t.Fatal("CompactStructuredJSONOutput() ok = false, want true")
	}
	if !strings.Contains(result.Output, `"truncated":true`) {
		t.Fatalf("result.Output = %q, want truncated marker", result.Output)
	}
	if !strings.Contains(result.Output, `"items_count":12`) {
		t.Fatalf("result.Output = %q, want items_count summary", result.Output)
	}
	if !strings.Contains(result.Output, `"items_preview"`) {
		t.Fatalf("result.Output = %q, want items preview", result.Output)
	}
}

func TestCompactStructuredJSONOutputReturnsFalseForInvalidJSON(t *testing.T) {
	t.Parallel()

	result, ok, err := CompactStructuredJSONOutput(`{"broken":`, JSONCompactOptions{Budget: DefaultJSONStubBytes})
	if err != nil {
		t.Fatalf("CompactStructuredJSONOutput() error = %v, want nil", err)
	}
	if ok {
		t.Fatalf("CompactStructuredJSONOutput() ok = %t, want false", ok)
	}
	if result.Output != "" {
		t.Fatalf("result.Output = %q, want empty output", result.Output)
	}
}

func TestCompactStructuredJSONOutputReturnsFalseForTrailingJSONValues(t *testing.T) {
	t.Parallel()

	result, ok, err := CompactStructuredJSONOutput(`{"ok":true}{"extra":true}`, JSONCompactOptions{Budget: DefaultJSONStubBytes})
	if err != nil {
		t.Fatalf("CompactStructuredJSONOutput() error = %v, want nil", err)
	}
	if ok {
		t.Fatalf("CompactStructuredJSONOutput() ok = %t, want false", ok)
	}
	if result.Output != "" {
		t.Fatalf("result.Output = %q, want empty output", result.Output)
	}
}
