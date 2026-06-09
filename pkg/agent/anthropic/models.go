// See LICENSE for licensing information

// models.go centralizes per-model capability checks and reasoning-level
// suffix parsing.

package anthropic

import "strings"

func parseModelAndReasoningLevel(model string) (string, string) {
	model = strings.TrimSpace(model)
	lastDash := strings.LastIndex(model, "-")
	if lastDash <= 0 || lastDash == len(model)-1 {
		return model, ""
	}
	base := strings.TrimSpace(model[:lastDash])
	level := strings.TrimSpace(model[lastDash+1:])
	if base == "" {
		return model, ""
	}
	switch level {
	case "low", "medium", "high", "max":
		return base, level
	default:
		return model, ""
	}
}

func supportsAnthropicStructuredOutputs(model string) bool {
	model = strings.TrimSpace(model)
	switch {
	case strings.HasPrefix(model, "claude-opus-4-6"),
		strings.HasPrefix(model, "claude-sonnet-4-6"),
		strings.HasPrefix(model, "claude-opus-4-5"),
		strings.HasPrefix(model, "claude-sonnet-4-5"),
		strings.HasPrefix(model, "claude-haiku-4-5"):
		return true
	default:
		return false
	}
}
