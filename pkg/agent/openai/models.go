// See LICENSE for licensing information

// models.go centralizes per-model capability checks and reasoning-effort
// suffix parsing.

package openai

import "strings"

func supportsResponsesStructuredTextFormat(model string) bool {
	model = strings.TrimSpace(model)
	switch {
	case strings.HasPrefix(model, "gpt-5"),
		strings.HasPrefix(model, "gpt-4.1"),
		strings.HasPrefix(model, "o1"),
		strings.HasPrefix(model, "o3"),
		strings.HasPrefix(model, "o4"):
		return true
	case model == "chatgpt-4o-latest",
		model == "gpt-4o",
		model == "gpt-4o-mini":
		return true
	case model == "gpt-4o-2024-05-13":
		return false
	case strings.HasPrefix(model, "gpt-4o-"):
		return model >= "gpt-4o-2024-08-06"
	case strings.HasPrefix(model, "gpt-4o-mini-"):
		return model >= "gpt-4o-mini-2024-07-18"
	default:
		return false
	}
}

func supportsResponsesReasoningSummary(model string) bool {
	model = strings.TrimSpace(model)
	switch {
	case strings.HasPrefix(model, "gpt-5"),
		strings.HasPrefix(model, "o1"),
		strings.HasPrefix(model, "o3"),
		strings.HasPrefix(model, "o4"):
		return true
	default:
		return false
	}
}

func supportsResponsesHostedWebSearch(model, effort string) bool {
	model = strings.TrimSpace(model)
	effort = strings.TrimSpace(effort)
	switch {
	case strings.HasPrefix(model, "gpt-5") && effort == "minimal":
		return false
	case strings.HasPrefix(model, "gpt-4.1-nano"):
		return false
	case strings.HasPrefix(model, "gpt-5"),
		strings.HasPrefix(model, "gpt-4.1"),
		strings.HasPrefix(model, "gpt-4o"),
		strings.HasPrefix(model, "o1"),
		strings.HasPrefix(model, "o3"),
		strings.HasPrefix(model, "o4"):
		return true
	default:
		return false
	}
}

// SplitModelReasoningEffort splits a configured model name into the base
// model and an optional reasoning-effort suffix. Efforts are written with an
// "-x" marker so they cannot collide with real model names: "gpt-5-xhigh"
// means model "gpt-5" with reasoning effort "high".
func SplitModelReasoningEffort(model string) (base, effort string) {
	model = strings.TrimSpace(model)
	for _, effort := range []string{"xhigh", "high", "medium", "minimal", "low", "none"} {
		suffix := "-x" + effort
		if base, ok := strings.CutSuffix(model, suffix); ok && strings.TrimSpace(base) != "" {
			return base, effort
		}
	}
	return model, ""
}
