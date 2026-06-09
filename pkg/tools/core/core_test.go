package core

import (
	"strings"
	"testing"
)

func TestDecodeToolInput(t *testing.T) {
	t.Parallel()

	type input struct {
		Path  string `json:"path"`
		Limit int    `json:"limit"`
	}

	t.Run("valid object", func(t *testing.T) {
		t.Parallel()
		got, err := DecodeToolInput[input](`{"path":"/tmp/x","limit":3}`)
		if err != nil {
			t.Fatalf("DecodeToolInput() error = %v", err)
		}
		if got.Path != "/tmp/x" || got.Limit != 3 {
			t.Fatalf("DecodeToolInput() = %+v, want populated fields", got)
		}
	})

	t.Run("empty input decodes zero value", func(t *testing.T) {
		t.Parallel()
		got, err := DecodeToolInput[input]("   ")
		if err != nil {
			t.Fatalf("DecodeToolInput() error = %v", err)
		}
		if got != (input{}) {
			t.Fatalf("DecodeToolInput() = %+v, want zero value", got)
		}
	})

	t.Run("unknown field rejected", func(t *testing.T) {
		t.Parallel()
		_, err := DecodeToolInput[input](`{"path":"/tmp/x","bogus":true}`)
		if err == nil || !strings.Contains(err.Error(), "invalid tool arguments") {
			t.Fatalf("DecodeToolInput() error = %v, want invalid-arguments failure", err)
		}
	})

	t.Run("trailing data rejected", func(t *testing.T) {
		t.Parallel()
		_, err := DecodeToolInput[input](`{"path":"/tmp/x"} {"path":"/tmp/y"}`)
		if err == nil || !strings.Contains(err.Error(), "trailing data") {
			t.Fatalf("DecodeToolInput() error = %v, want trailing-data failure", err)
		}
	})

	t.Run("wrong type rejected", func(t *testing.T) {
		t.Parallel()
		_, err := DecodeToolInput[input](`{"limit":"three"}`)
		if err == nil || !strings.Contains(err.Error(), "invalid tool arguments") {
			t.Fatalf("DecodeToolInput() error = %v, want type failure", err)
		}
	})

	t.Run("malformed json rejected", func(t *testing.T) {
		t.Parallel()
		_, err := DecodeToolInput[input](`{"path":`)
		if err == nil {
			t.Fatal("DecodeToolInput() error = nil, want parse failure")
		}
	})
}

func TestUsageAdd(t *testing.T) {
	t.Parallel()

	got := Usage{InputTokens: 10, OutputTokens: 2, CachedTokens: 5}.
		Add(Usage{InputTokens: 7, OutputTokens: 3, CachedTokens: 1})
	want := Usage{InputTokens: 17, OutputTokens: 5, CachedTokens: 6}
	if got != want {
		t.Fatalf("Usage.Add() = %+v, want %+v", got, want)
	}
}
