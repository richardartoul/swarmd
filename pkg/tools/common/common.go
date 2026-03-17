package common

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
)

func ObjectSchema(properties map[string]any, required ...string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             append([]string{}, required...),
		"additionalProperties": false,
	}
}

func StringSchema(description string) map[string]any {
	return map[string]any{
		"type":        "string",
		"description": description,
	}
}

func NumberSchema(description string) map[string]any {
	return map[string]any{
		"type":        "number",
		"description": description,
	}
}

func BooleanSchema(description string) map[string]any {
	return map[string]any{
		"type":        "boolean",
		"description": description,
	}
}

func StringMapSchema(description string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"description":          description,
		"additionalProperties": map[string]any{"type": "string"},
	}
}

func IntegerSchema(description string) map[string]any {
	return map[string]any{
		"type":        "integer",
		"description": description,
	}
}

func StringEnumSchema(description string, values ...string) map[string]any {
	schema := StringSchema(description)
	schema["enum"] = append([]string(nil), values...)
	return schema
}

func ToolInterop(mcpName string, preferredKind, fallbackKind toolscore.ToolBoundaryKind, fallbackName string) toolscore.ToolInterop {
	return toolscore.ToolInterop{
		MCPToolName:            mcpName,
		OpenAIPreferredKind:    preferredKind,
		OpenAIFallbackKind:     fallbackKind,
		OpenAIFallbackToolName: fallbackName,
	}
}

func ValidateNoToolConfig(toolID string, raw map[string]any) error {
	if len(raw) == 0 {
		return nil
	}
	return fmt.Errorf("tool %q does not accept config", toolID)
}

func DecodeToolConfig[T any](raw map[string]any) (T, error) {
	var config T
	if len(raw) == 0 {
		return config, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return config, fmt.Errorf("marshal tool config: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return config, fmt.Errorf("decode tool config: %w", err)
	}
	return config, nil
}

func MarshalToolOutput(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal tool output: %w", err)
	}
	return string(encoded), nil
}

func AppendUniqueString(values []string, value string) []string {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}

func FirstNonEmptyString(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func ClampInt(value, defaultValue, maxValue int) int {
	switch {
	case value <= 0:
		return defaultValue
	case maxValue > 0 && value > maxValue:
		return maxValue
	default:
		return value
	}
}

func MinInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func MaxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func BoundedDurationMillis(value int, defaultValue, ceiling time.Duration) time.Duration {
	var duration time.Duration
	switch {
	case value > 0:
		duration = time.Duration(value) * time.Millisecond
	default:
		duration = defaultValue
	}
	if ceiling > 0 && (duration == 0 || duration > ceiling) {
		return ceiling
	}
	return duration
}

func ReadTextFileLimited(fs sandbox.FileSystem, path string, limit int64) (string, error) {
	file, err := fs.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	if limit <= 0 {
		return "", fmt.Errorf("read limit must be positive")
	}
	reader := io.LimitReader(file, limit+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	if int64(len(data)) > limit {
		return "", fmt.Errorf("%q exceeded the read limit of %d bytes", path, limit)
	}
	return string(data), nil
}

func SplitPreservingLineStructure(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func RenderNumberedLines(b *strings.Builder, lines []string, startLine int) {
	for idx, line := range lines {
		fmt.Fprintf(b, "%d|%s\n", startLine+idx, line)
	}
}

func ScannerLines(text string) []string {
	if text == "" {
		return nil
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}
