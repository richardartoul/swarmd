// See LICENSE for licensing information

// tools.go adapts runtime tool definitions to Anthropic tool schemas,
// including schema compatibility rewrites and input examples.

package anthropic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/richardartoul/swarmd/pkg/agent"
)

type anthropicTool struct {
	Name          string                 `json:"name"`
	Description   string                 `json:"description,omitempty"`
	Strict        bool                   `json:"strict,omitempty"`
	InputSchema   map[string]any         `json:"input_schema"`
	InputExamples []map[string]any       `json:"input_examples,omitempty"`
	CacheControl  *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicToolChoice struct {
	Type                   string `json:"type"`
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use,omitempty"`
}

func buildAnthropicTools(tools []agent.ToolDefinition, promptCacheTTL string, cacheStaticPrefix bool) []anthropicTool {
	if len(tools) == 0 {
		return nil
	}
	result := make([]anthropicTool, 0, len(tools))
	for _, tool := range tools {
		result = append(result, anthropicTool{
			Name:          tool.Name,
			Description:   anthropicToolDescription(tool),
			Strict:        tool.Strict,
			InputSchema:   anthropicToolSchema(tool),
			InputExamples: anthropicToolInputExamples(tool),
		})
	}
	if cacheStaticPrefix && len(result) > 0 && promptCacheTTL != "" {
		result[len(result)-1].CacheControl = anthropicPromptCacheControl(promptCacheTTL)
	}
	return result
}

func anthropicToolDescription(tool agent.ToolDefinition) string {
	sections := make([]string, 0, 4)
	if description := strings.TrimSpace(tool.Description); description != "" {
		sections = append(sections, description)
	}
	if format := anthropicToolFormatLabel(tool.CustomFormat); format != "" {
		sections = append(sections, "Input format: "+format+".")
	}
	if notes := strings.TrimSpace(tool.OutputNotes); notes != "" {
		sections = append(sections, "Output notes: "+notes)
	}
	if tags := strings.TrimSpace(strings.Join(tool.SafetyTags, ", ")); tags != "" {
		sections = append(sections, "Safety tags: "+tags+".")
	}
	return strings.Join(sections, "\n\n")
}

func anthropicToolFormatLabel(format *agent.ToolFormat) string {
	if format == nil {
		return ""
	}
	typeName := strings.TrimSpace(format.Type)
	syntax := strings.TrimSpace(format.Syntax)
	switch {
	case typeName != "" && syntax != "":
		return typeName + "/" + syntax
	case typeName != "":
		return typeName
	case syntax != "":
		return syntax
	default:
		return ""
	}
}

func anthropicToolInputExamples(tool agent.ToolDefinition) []map[string]any {
	if len(tool.Examples) == 0 {
		return nil
	}
	limit := anthropicToolInputExampleLimit(tool)
	if limit == 0 {
		return nil
	}
	examples := make([]map[string]any, 0, min(limit, len(tool.Examples)))
	for _, example := range tool.Examples {
		if payload, ok := anthropicToolInputExample(tool, example); ok {
			examples = append(examples, payload)
			if len(examples) == limit {
				break
			}
		}
	}
	if len(examples) == 0 {
		return nil
	}
	return examples
}

func anthropicToolInputExampleLimit(tool agent.ToolDefinition) int {
	switch tool.Kind {
	case agent.ToolKindFunction, agent.ToolKindCustom:
		return 1
	default:
		return 0
	}
}

func anthropicToolInputExample(tool agent.ToolDefinition, example string) (map[string]any, bool) {
	example = strings.TrimSpace(example)
	if example == "" {
		return nil, false
	}
	if tool.Kind == agent.ToolKindCustom {
		return map[string]any{anthropicCustomToolInputField(tool): example}, true
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(example), &payload); err != nil || payload == nil {
		return nil, false
	}
	return payload, true
}

func anthropicToolSchema(tool agent.ToolDefinition) map[string]any {
	if tool.Kind == agent.ToolKindCustom {
		if tool.Name == agent.ToolNameApplyPatch {
			return map[string]any{
				"type": "object",
				"properties": map[string]any{
					"patch": map[string]any{
						"type":        "string",
						"description": "Structured patch text. Provide the full patch body including \"*** Begin Patch\" and \"*** End Patch\".",
					},
				},
				"required":             []string{"patch"},
				"additionalProperties": false,
			}
		}
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				anthropicCustomToolInputField(tool): map[string]any{
					"type":        "string",
					"description": "Freeform custom tool input.",
				},
			},
			"required":             []string{anthropicCustomToolInputField(tool)},
			"additionalProperties": false,
		}
	}
	if len(tool.Parameters) != 0 {
		return anthropicCompatibleInputSchema(tool.Parameters)
	}
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"required":             []string{},
		"additionalProperties": false,
	}
}

func anthropicCompatibleInputSchema(schema map[string]any) map[string]any {
	if len(schema) == 0 {
		return schema
	}
	var notes []string
	for _, keyword := range []string{"oneOf", "anyOf", "allOf"} {
		value, ok := schema[keyword]
		if !ok {
			continue
		}
		notes = append(notes, anthropicTopLevelSchemaConstraintNote(keyword, value))
	}
	if len(notes) == 0 {
		return schema
	}
	clone := make(map[string]any, len(schema))
	for key, value := range schema {
		clone[key] = value
	}
	delete(clone, "oneOf")
	delete(clone, "anyOf")
	delete(clone, "allOf")
	description, _ := clone["description"].(string)
	description = strings.TrimSpace(description)
	note := strings.Join(notes, " ")
	switch {
	case description == "":
		clone["description"] = note
	default:
		clone["description"] = description + "\n\n" + note
	}
	return clone
}

func anthropicTopLevelSchemaConstraintNote(keyword string, value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("Anthropic compatibility note: obey the original top-level %s constraint when constructing tool input.", keyword)
	}
	return fmt.Sprintf("Anthropic compatibility note: obey the original top-level %s constraint when constructing tool input: %s", keyword, string(encoded))
}

func anthropicCustomToolInputField(tool agent.ToolDefinition) string {
	if tool.Name == agent.ToolNameApplyPatch {
		return "patch"
	}
	return "input"
}

func allowedToolDefinition(allowedTools []agent.ToolDefinition, name string) (agent.ToolDefinition, bool) {
	for _, tool := range allowedTools {
		if tool.Name == name {
			return tool, true
		}
	}
	return agent.ToolDefinition{}, false
}

func compactJSON(value any) string {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "{}"
	}
	return strings.TrimSpace(b.String())
}
