package common

import (
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
