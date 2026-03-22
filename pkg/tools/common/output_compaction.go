package common

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
)

const (
	DefaultJSONStubBytes       = 8 << 10
	jsonStubKind               = "json_stub_v1"
	jsonStubMaxDepth           = 2
	jsonStubPreviewItems       = 3
	jsonStubPreviewStringRunes = 384
)

// JSONCompactOptions controls how oversized structured JSON outputs are compacted.
type JSONCompactOptions struct {
	Budget         int
	FullOutputFile *toolscore.FileReference
}

// JSONCompactResult is one bounded replay-safe JSON stub.
type JSONCompactResult struct {
	Output    string
	Files     []toolscore.FileReference
	Compacted bool
}

type jsonStubContext struct {
	AllFiles      []toolscore.FileReference
	StubFiles     []toolscore.FileReference
	OriginalBytes int
}

type jsonStubCandidate struct {
	Value any
	Files []toolscore.FileReference
}

// NormalizeFileReferences trims, deduplicates, and stabilizes file references.
func NormalizeFileReferences(files []toolscore.FileReference) []toolscore.FileReference {
	if len(files) == 0 {
		return []toolscore.FileReference{}
	}
	normalized := make([]toolscore.FileReference, 0, len(files))
	seen := make(map[string]struct{}, len(files))
	for _, file := range files {
		path := strings.TrimSpace(file.Path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		normalized = append(normalized, toolscore.FileReference{
			Path:        path,
			MimeType:    strings.TrimSpace(file.MimeType),
			Description: strings.TrimSpace(file.Description),
			SizeBytes:   file.SizeBytes,
		})
	}
	if len(normalized) == 0 {
		return []toolscore.FileReference{}
	}
	return normalized
}

// CompactStructuredJSONOutput turns a large JSON payload into a bounded replay-safe stub.
func CompactStructuredJSONOutput(raw string, opts JSONCompactOptions) (JSONCompactResult, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return JSONCompactResult{}, false, nil
	}
	parsed, ok, err := decodeJSONValue(raw)
	if err != nil {
		return JSONCompactResult{}, false, err
	}
	if !ok {
		return JSONCompactResult{}, false, nil
	}
	budget := opts.Budget
	if budget <= 0 {
		budget = DefaultJSONStubBytes
	}

	allFiles := extractTopLevelFileReferences(parsed)
	if opts.FullOutputFile != nil {
		allFiles = append(allFiles, *opts.FullOutputFile)
	}
	allFiles = NormalizeFileReferences(allFiles)
	stubFiles := compactStubFileReferences(allFiles, opts.FullOutputFile)
	ctx := jsonStubContext{
		AllFiles:      allFiles,
		StubFiles:     stubFiles,
		OriginalBytes: len(raw),
	}

	candidates := []jsonStubCandidate{{
		Value: buildJSONStub(parsed, ctx, true),
		Files: ctx.StubFiles,
	}, {
		Value: buildJSONStub(parsed, ctx, false),
		Files: ctx.StubFiles,
	}, {
		Value: buildMinimalJSONStub(parsed, ctx),
		Files: ctx.StubFiles,
	}}
	if opts.FullOutputFile != nil {
		minimalCtx := ctx
		minimalCtx.StubFiles = []toolscore.FileReference{*opts.FullOutputFile}
		candidates = append(candidates, jsonStubCandidate{
			Value: buildMinimalJSONStub(parsed, minimalCtx),
			Files: minimalCtx.StubFiles,
		})
	}

	var last string
	lastFiles := ctx.StubFiles
	for _, candidate := range candidates {
		encoded, err := marshalCompactJSON(candidate.Value)
		if err != nil {
			return JSONCompactResult{}, true, err
		}
		last = encoded
		lastFiles = candidate.Files
		if len(encoded) <= budget {
			return JSONCompactResult{
				Output:    encoded,
				Files:     candidate.Files,
				Compacted: encoded != raw,
			}, true, nil
		}
	}
	return JSONCompactResult{
		Output:    last,
		Files:     lastFiles,
		Compacted: last != raw,
	}, true, nil
}

func decodeJSONValue(raw string) (any, bool, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false, nil
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return nil, false, nil
	} else if err != io.EOF {
		return nil, false, nil
	}
	return value, true, nil
}

func extractTopLevelFileReferences(value any) []toolscore.FileReference {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := object["files"]
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	files := make([]toolscore.FileReference, 0, len(list))
	for _, item := range list {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		path, _ := object["path"].(string)
		if strings.TrimSpace(path) == "" {
			continue
		}
		file := toolscore.FileReference{Path: strings.TrimSpace(path)}
		if mimeType, ok := object["mime_type"].(string); ok {
			file.MimeType = strings.TrimSpace(mimeType)
		}
		if description, ok := object["description"].(string); ok {
			file.Description = strings.TrimSpace(description)
		}
		switch size := object["size_bytes"].(type) {
		case json.Number:
			if parsed, err := size.Int64(); err == nil {
				file.SizeBytes = parsed
			}
		case float64:
			file.SizeBytes = int64(size)
		}
		files = append(files, file)
	}
	return NormalizeFileReferences(files)
}

func compactStubFileReferences(files []toolscore.FileReference, fullOutputFile *toolscore.FileReference) []toolscore.FileReference {
	files = NormalizeFileReferences(files)
	if len(files) <= jsonStubPreviewItems {
		return files
	}
	fullOutputPath := ""
	if fullOutputFile != nil {
		fullOutputPath = strings.TrimSpace(fullOutputFile.Path)
	}
	limit := jsonStubPreviewItems
	if fullOutputPath != "" {
		limit--
	}
	preview := make([]toolscore.FileReference, 0, jsonStubPreviewItems)
	for _, file := range files {
		if fullOutputPath != "" && file.Path == fullOutputPath {
			continue
		}
		preview = append(preview, file)
		if len(preview) >= limit {
			break
		}
	}
	if fullOutputPath != "" {
		preview = append(preview, *fullOutputFile)
	}
	return NormalizeFileReferences(preview)
}

func buildJSONStub(value any, ctx jsonStubContext, includePreviews bool) any {
	switch current := value.(type) {
	case map[string]any:
		if looksLikeToolEnvelope(current) {
			return compactKnownEnvelope(current, ctx, includePreviews)
		}
		return compactGenericObject(current, ctx, includePreviews, "")
	case []any:
		return compactTopLevelArray(current, ctx, includePreviews)
	default:
		return compactTopLevelScalar(current, ctx)
	}
}

func buildMinimalJSONStub(value any, ctx jsonStubContext) any {
	out := map[string]any{
		"truncated": true,
		"_meta":     jsonStubMetadata(ctx.OriginalBytes, nil),
	}
	if summary := minimalSummary(value); summary != nil {
		out["summary"] = summary
	}
	if len(ctx.StubFiles) > 0 {
		out["files"] = ctx.StubFiles
		if len(ctx.AllFiles) > len(ctx.StubFiles) {
			out["files_count"] = len(ctx.AllFiles)
		}
	}
	return out
}

func looksLikeToolEnvelope(object map[string]any) bool {
	_, hasTool := object["tool"]
	_, hasAction := object["action"]
	_, hasOK := object["ok"]
	return hasTool && hasAction && hasOK
}

func compactKnownEnvelope(object map[string]any, ctx jsonStubContext, includePreviews bool) map[string]any {
	out := make(map[string]any)
	omitted := make([]string, 0)
	for _, key := range []string{"tool", "action", "ok", "status", "error", "warnings", "page_info", "next_cursor", "next_offset", "returned_count", "truncated"} {
		value, ok := object[key]
		if !ok {
			continue
		}
		compactField(out, key, value, 0, includePreviews, "summary."+key, &omitted, true)
	}
	if len(ctx.StubFiles) > 0 {
		out["files"] = ctx.StubFiles
		if len(ctx.AllFiles) > len(ctx.StubFiles) {
			out["files_count"] = len(ctx.AllFiles)
			omitted = append(omitted, "files")
		}
	}
	if data, ok := object["data"]; ok {
		compactField(out, "data", data, 0, includePreviews, "data", &omitted, true)
	}
	for _, key := range sortedKeys(object) {
		if key == "data" || key == "files" {
			continue
		}
		if _, exists := out[key]; exists {
			continue
		}
		compactField(out, key, object[key], 0, includePreviews, key, &omitted, true)
	}
	out["_meta"] = jsonStubMetadata(ctx.OriginalBytes, omitted)
	return out
}

func compactGenericObject(object map[string]any, ctx jsonStubContext, includePreviews bool, path string) map[string]any {
	out := map[string]any{
		"truncated": true,
	}
	omitted := make([]string, 0)
	for _, key := range sortedKeys(object) {
		if key == "files" {
			continue
		}
		compactField(out, key, object[key], 0, includePreviews, joinJSONPath(path, key), &omitted, false)
	}
	if len(ctx.StubFiles) > 0 {
		out["files"] = ctx.StubFiles
		if len(ctx.AllFiles) > len(ctx.StubFiles) {
			out["files_count"] = len(ctx.AllFiles)
			omitted = append(omitted, joinJSONPath(path, "files"))
		}
	}
	out["_meta"] = jsonStubMetadata(ctx.OriginalBytes, omitted)
	return out
}

func compactTopLevelArray(values []any, ctx jsonStubContext, includePreviews bool) map[string]any {
	out := map[string]any{
		"truncated":   true,
		"items_count": len(values),
	}
	if includePreviews && len(values) > 0 {
		out["items_preview"] = previewArray(values, 0, includePreviews)
	}
	if len(ctx.StubFiles) > 0 {
		out["files"] = ctx.StubFiles
		if len(ctx.AllFiles) > len(ctx.StubFiles) {
			out["files_count"] = len(ctx.AllFiles)
		}
	}
	out["_meta"] = jsonStubMetadata(ctx.OriginalBytes, []string{"items"})
	return out
}

func compactTopLevelScalar(value any, ctx jsonStubContext) map[string]any {
	out := map[string]any{
		"truncated":     true,
		"value_preview": compactScalar(value),
		"_meta":         jsonStubMetadata(ctx.OriginalBytes, []string{"value"}),
	}
	if len(ctx.StubFiles) > 0 {
		out["files"] = ctx.StubFiles
		if len(ctx.AllFiles) > len(ctx.StubFiles) {
			out["files_count"] = len(ctx.AllFiles)
		}
	}
	return out
}

func compactField(out map[string]any, key string, value any, depth int, includePreviews bool, path string, omitted *[]string, preserveKnownKey bool) {
	switch current := value.(type) {
	case nil, bool, json.Number, float64, string:
		out[key] = compactScalar(current)
	case []any:
		if includePreviews && len(current) <= jsonStubPreviewItems && depth < jsonStubMaxDepth {
			out[key] = previewArray(current, depth, includePreviews)
			return
		}
		countKey := key + "_count"
		previewKey := key + "_preview"
		if preserveKnownKey && key == "warnings" {
			if includePreviews {
				out[key] = previewArray(current, depth, includePreviews)
			} else {
				out[key] = []any{}
			}
			out[countKey] = len(current)
		} else {
			out[countKey] = len(current)
			if includePreviews && len(current) > 0 {
				out[previewKey] = previewArray(current, depth, includePreviews)
			}
		}
		*omitted = append(*omitted, path)
	case map[string]any:
		if depth >= jsonStubMaxDepth {
			out[key] = compactLeafObject(current, includePreviews)
			*omitted = append(*omitted, path)
			return
		}
		child := make(map[string]any)
		childOmitted := make([]string, 0)
		for _, childKey := range sortedKeys(current) {
			compactField(child, childKey, current[childKey], depth+1, includePreviews, joinJSONPath(path, childKey), &childOmitted, false)
		}
		out[key] = child
		*omitted = append(*omitted, childOmitted...)
	default:
		out[key] = fmt.Sprintf("%v", current)
	}
}

func compactLeafObject(object map[string]any, includePreviews bool) map[string]any {
	out := make(map[string]any)
	for _, key := range sortedKeys(object) {
		switch current := object[key].(type) {
		case nil, bool, json.Number, float64, string:
			out[key] = compactScalar(current)
		case []any:
			out[key+"_count"] = len(current)
			if includePreviews && len(current) > 0 {
				out[key+"_preview"] = previewArray(current, jsonStubMaxDepth, includePreviews)
			}
		case map[string]any:
			out[key] = map[string]any{
				"truncated":   true,
				"field_count": len(current),
			}
		default:
			out[key] = fmt.Sprintf("%v", current)
		}
	}
	return out
}

func previewArray(values []any, depth int, includePreviews bool) []any {
	limit := MinInt(len(values), jsonStubPreviewItems)
	preview := make([]any, 0, limit)
	for idx := 0; idx < limit; idx++ {
		preview = append(preview, previewValue(values[idx], depth+1, includePreviews))
	}
	return preview
}

func previewValue(value any, depth int, includePreviews bool) any {
	switch current := value.(type) {
	case nil, bool, json.Number, float64, string:
		return compactScalar(current)
	case []any:
		if depth >= jsonStubMaxDepth {
			return map[string]any{"count": len(current)}
		}
		if !includePreviews {
			return map[string]any{"count": len(current)}
		}
		if len(current) <= jsonStubPreviewItems {
			return previewArray(current, depth, includePreviews)
		}
		return map[string]any{
			"count":   len(current),
			"preview": previewArray(current, depth, includePreviews),
		}
	case map[string]any:
		if depth >= jsonStubMaxDepth {
			return compactLeafObject(current, includePreviews)
		}
		child := make(map[string]any)
		for _, key := range sortedKeys(current) {
			compactField(child, key, current[key], depth, includePreviews, key, &[]string{}, false)
		}
		return child
	default:
		return fmt.Sprintf("%v", current)
	}
}

func compactScalar(value any) any {
	switch current := value.(type) {
	case string:
		return truncatePreviewString(strings.TrimSpace(current), jsonStubPreviewStringRunes)
	default:
		return current
	}
}

func truncatePreviewString(value string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	if maxRunes <= 1 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-1]) + "…"
}

func jsonStubMetadata(originalBytes int, omitted []string) map[string]any {
	normalized := normalizeOmittedFields(omitted)
	return map[string]any{
		"kind":           jsonStubKind,
		"compacted":      true,
		"original_bytes": originalBytes,
		"omitted_fields": normalized,
	}
}

func normalizeOmittedFields(paths []string) []string {
	normalized := normalizeStringSlice(paths)
	if len(normalized) == 0 {
		return []string{}
	}
	slices.Sort(normalized)
	deduped := normalized[:0]
	for _, current := range normalized {
		if len(deduped) > 0 && deduped[len(deduped)-1] == current {
			continue
		}
		deduped = append(deduped, current)
	}
	return deduped
}

func normalizeStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		normalized = append(normalized, value)
	}
	return normalized
}

func joinJSONPath(parent, key string) string {
	key = strings.TrimSpace(key)
	if parent == "" {
		return key
	}
	if key == "" {
		return parent
	}
	return parent + "." + key
}

func minimalSummary(value any) map[string]any {
	switch current := value.(type) {
	case map[string]any:
		summary := make(map[string]any)
		for _, key := range []string{"tool", "action", "ok", "status", "next_cursor", "next_offset"} {
			if raw, ok := current[key]; ok {
				summary[key] = compactScalar(raw)
			}
		}
		if _, ok := current["warnings"]; ok {
			summary["warnings_present"] = true
		}
		if data, ok := current["data"]; ok {
			addCollectionCounts(summary, data, "items")
		}
		if len(summary) == 0 {
			summary["field_count"] = len(current)
		}
		return summary
	case []any:
		return map[string]any{"items_count": len(current)}
	default:
		return map[string]any{"type": fmt.Sprintf("%T", current)}
	}
}

func addCollectionCounts(summary map[string]any, value any, defaultKey string) {
	switch current := value.(type) {
	case []any:
		summary[defaultKey+"_count"] = len(current)
	case map[string]any:
		for _, key := range sortedKeys(current) {
			if array, ok := current[key].([]any); ok {
				summary[key+"_count"] = len(array)
			}
		}
	}
}

func sortedKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func marshalCompactJSON(value any) (string, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", fmt.Errorf("marshal compacted structured output: %w", err)
	}
	return strings.TrimSpace(buffer.String()), nil
}
