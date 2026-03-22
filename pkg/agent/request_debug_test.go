package agent

import (
	"bytes"
	"testing"
)

func TestMaybeWriteDebugPromptWritesExactPayloadWhenEnabled(t *testing.T) {
	t.Setenv(DebugPromptEnvVar, "1")

	var out bytes.Buffer
	previous := debugPromptOutput
	debugPromptOutput = &out
	t.Cleanup(func() {
		debugPromptOutput = previous
	})

	payload := []byte("{\"messages\":[{\"role\":\"user\",\"content\":\"hello <world>\"}]}\n")
	MaybeWriteDebugPrompt(payload)

	if got := out.String(); got != string(payload) {
		t.Fatalf("MaybeWriteDebugPrompt() output = %q, want %q", got, string(payload))
	}
}

func TestMaybeWriteDebugPromptSkipsWhenDisabled(t *testing.T) {
	t.Setenv(DebugPromptEnvVar, "")

	var out bytes.Buffer
	previous := debugPromptOutput
	debugPromptOutput = &out
	t.Cleanup(func() {
		debugPromptOutput = previous
	})

	MaybeWriteDebugPrompt([]byte("{\"messages\":[]}\n"))

	if got := out.String(); got != "" {
		t.Fatalf("MaybeWriteDebugPrompt() output = %q, want empty output", got)
	}
}

func TestMaybeWriteDebugResponseWritesExactPayloadWhenEnabled(t *testing.T) {
	t.Setenv(DebugResponseEnvVar, "1")

	var out bytes.Buffer
	previous := debugResponseOutput
	debugResponseOutput = &out
	t.Cleanup(func() {
		debugResponseOutput = previous
	})

	payload := []byte("{\"id\":\"msg_123\",\"content\":[{\"type\":\"text\",\"text\":\"hello <world>\"}]}")
	MaybeWriteDebugResponse(payload)

	if got := out.String(); got != string(payload) {
		t.Fatalf("MaybeWriteDebugResponse() output = %q, want %q", got, string(payload))
	}
}

func TestMaybeWriteDebugResponseSkipsWhenDisabled(t *testing.T) {
	t.Setenv(DebugResponseEnvVar, "")

	var out bytes.Buffer
	previous := debugResponseOutput
	debugResponseOutput = &out
	t.Cleanup(func() {
		debugResponseOutput = previous
	})

	MaybeWriteDebugResponse([]byte("{\"id\":\"msg_123\"}"))

	if got := out.String(); got != "" {
		t.Fatalf("MaybeWriteDebugResponse() output = %q, want empty output", got)
	}
}
