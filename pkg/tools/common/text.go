package common

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
)

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
