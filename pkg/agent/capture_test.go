// See LICENSE for licensing information

package agent

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateTextNeverSplitsRunes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		text      string
		limit     int
		want      string
		truncated bool
	}{
		{name: "under limit untouched", text: "héllo", limit: 64, want: "héllo"},
		{name: "ascii cut at limit", text: "abcdef", limit: 3, want: "abc", truncated: true},
		{name: "cut lands after complete rune", text: "héx", limit: 3, want: "hé", truncated: true},
		{name: "cut splits two byte rune", text: "héx", limit: 2, want: "h", truncated: true},
		{name: "cut splits four byte rune", text: "ab\U0001F389cd", limit: 4, want: "ab", truncated: true},
		{name: "cut mid four byte rune late", text: "ab\U0001F389cd", limit: 5, want: "ab", truncated: true},
		{name: "exact length untouched", text: "héllo", limit: len("héllo"), want: "héllo"},
		{name: "invalid bytes preserved", text: "ab\xff\xfe\xfdcd", limit: 4, want: "ab\xff\xfe", truncated: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, truncated := truncateText(test.text, test.limit)
			if got != test.want || truncated != test.truncated {
				t.Fatalf(
					"truncateText(%q, %d) = (%q, %t), want (%q, %t)",
					test.text, test.limit, got, truncated, test.want, test.truncated,
				)
			}
			if len(got) > test.limit && test.limit > 0 {
				t.Fatalf("truncateText(%q, %d) returned %d bytes, want <= limit", test.text, test.limit, len(got))
			}
		})
	}
}

// TestTruncateTextAlwaysValidOnValidInput sweeps every cut point of a
// multi-byte string to prove no limit can produce invalid UTF-8.
func TestTruncateTextAlwaysValidOnValidInput(t *testing.T) {
	t.Parallel()

	text := strings.Repeat("é🎉ü√", 8)
	for limit := 1; limit <= len(text); limit++ {
		got, _ := truncateText(text, limit)
		if !utf8.ValidString(got) {
			t.Fatalf("truncateText(..., %d) = %q is not valid UTF-8", limit, got)
		}
	}
}

// TestCaptureWriterPreviewStaysValidUTF8 pins the same guarantee for shell
// stream previews, whose byte budget is applied during streaming writes.
func TestCaptureWriterPreviewStaysValidUTF8(t *testing.T) {
	t.Parallel()

	text := strings.Repeat("\U0001F389", 16) // 64 bytes of 4-byte runes
	for limit := 1; limit < len(text); limit++ {
		writer := newCaptureWriter(limit, 0, nil, nil)
		// Stream in awkward chunk sizes so writes split runes.
		for start := 0; start < len(text); start += 3 {
			end := min(start+3, len(text))
			if _, err := writer.Write([]byte(text[start:end])); err != nil {
				t.Fatalf("Write() error = %v", err)
			}
		}
		snapshot, err := writer.Snapshot()
		if err != nil {
			t.Fatalf("Snapshot() error = %v", err)
		}
		if !snapshot.Truncated {
			t.Fatalf("limit %d: snapshot.Truncated = false, want true", limit)
		}
		if !utf8.ValidString(snapshot.Preview) {
			t.Fatalf("limit %d: preview %q is not valid UTF-8", limit, snapshot.Preview)
		}
		if snapshot.TotalBytes != int64(len(text)) {
			t.Fatalf("limit %d: TotalBytes = %d, want %d", limit, snapshot.TotalBytes, len(text))
		}
	}
}
