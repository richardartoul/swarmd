package server

import (
	"testing"
	"time"

	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

func TestNormalizedStepTimeoutDefaultsToFiveMinutes(t *testing.T) {
	t.Parallel()

	if got := normalizedStepTimeout(cpstore.CreateAgentParams{}); got != cpstore.DefaultAgentStepTimeout {
		t.Fatalf("normalizedStepTimeout() = %v, want %v", got, cpstore.DefaultAgentStepTimeout)
	}
}

func TestNormalizedStepTimeoutPreservesExplicitValue(t *testing.T) {
	t.Parallel()

	want := 45 * time.Second
	if got := normalizedStepTimeout(cpstore.CreateAgentParams{StepTimeout: want}); got != want {
		t.Fatalf("normalizedStepTimeout() = %v, want %v", got, want)
	}
}
