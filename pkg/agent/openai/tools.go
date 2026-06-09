// See LICENSE for licensing information

// tools.go adapts runtime tool definitions to the provider's wire shapes
// (function, custom, hosted) based on per-model capabilities.

package openai

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/richardartoul/swarmd/pkg/agent"
)

type responsesTool struct {
	Type        string           `json:"type"`
	Name        string           `json:"name,omitempty"`
	Description string           `json:"description,omitempty"`
	Strict      bool             `json:"strict,omitempty"`
	Parameters  map[string]any   `json:"parameters,omitempty"`
	Format      *responsesFormat `json:"format,omitempty"`
}

type responsesFormat struct {
	Type       string `json:"type,omitempty"`
	Syntax     string `json:"syntax,omitempty"`
	Definition string `json:"definition,omitempty"`
}

type openAIAdapterCapabilities struct {
	SupportsCustomTools     bool
	SupportsLocalShell      bool
	SupportsHostedWebSearch bool
}

type openAIToolAdapter struct {
	InternalName string
	ExposedName  string
	BoundaryKind agent.ToolBoundaryKind
	Tool         agent.ToolDefinition
}

func buildResponsesTools(tools []agent.ToolDefinition, caps openAIAdapterCapabilities) []responsesTool {
	if len(tools) == 0 {
		return nil
	}
	result := make([]responsesTool, 0, len(tools))
	for _, adapter := range buildOpenAIToolAdapters(tools, caps) {
		tool := adapter.Tool
		responseTool := responsesTool{
			Type:        string(adapter.BoundaryKind),
			Name:        adapter.ExposedName,
			Description: openAIToolDescription(tool),
		}
		switch adapter.BoundaryKind {
		case agent.ToolBoundaryKindCustom:
			if tool.CustomFormat != nil {
				responseTool.Format = &responsesFormat{
					Type:       tool.CustomFormat.Type,
					Syntax:     tool.CustomFormat.Syntax,
					Definition: tool.CustomFormat.Definition,
				}
			}
		case agent.ToolBoundaryKindWebSearch:
			responseTool.Name = ""
			responseTool.Description = ""
		default:
			responseTool.Type = string(agent.ToolBoundaryKindFunction)
			responseTool.Strict = tool.Strict
			responseTool.Parameters = openAICompatibleInputSchema(tool.Parameters)
			if len(responseTool.Parameters) == 0 {
				responseTool.Parameters = map[string]any{
					"type":                 "object",
					"properties":           map[string]any{},
					"required":             []string{},
					"additionalProperties": false,
				}
			}
		}
		result = append(result, responseTool)
	}
	return result
}

func openAICompatibleInputSchema(schema map[string]any) map[string]any {
	if len(schema) == 0 {
		return schema
	}
	var notes []string
	for _, keyword := range []string{"oneOf", "anyOf", "allOf", "enum", "not"} {
		value, ok := schema[keyword]
		if !ok {
			continue
		}
		notes = append(notes, openAITopLevelSchemaConstraintNote(keyword, value))
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
	delete(clone, "enum")
	delete(clone, "not")
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

func openAITopLevelSchemaConstraintNote(keyword string, value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("OpenAI compatibility note: obey the original top-level %s constraint when constructing tool input.", keyword)
	}
	return fmt.Sprintf("OpenAI compatibility note: obey the original top-level %s constraint when constructing tool input: %s", keyword, string(encoded))
}

func hasOpenAIBuiltInTools(tools []agent.ToolDefinition, caps openAIAdapterCapabilities) bool {
	for _, adapter := range buildOpenAIToolAdapters(tools, caps) {
		switch adapter.BoundaryKind {
		case agent.ToolBoundaryKindWebSearch, agent.ToolBoundaryKindLocalShell:
			return true
		}
	}
	return false
}

func openAIReplayCallNameAndArguments(replay agent.StepReplay, adapter openAIToolAdapter, ok bool) (string, string) {
	name := replay.ToolName
	tool := agent.ToolDefinition{
		Name: replay.ToolName,
		Kind: replay.ToolKind,
	}
	if ok {
		name = adapter.ExposedName
		tool = adapter.Tool
	}
	if replay.ToolKind == agent.ToolKindCustom && openAIReplayBoundaryKind(replay, adapter, ok) != agent.ToolBoundaryKindCustom {
		return name, openAICustomToolArguments(tool, replay.Input)
	}
	arguments := strings.TrimSpace(replay.Input)
	if arguments == "" && replay.ToolKind != agent.ToolKindCustom {
		arguments = "{}"
	}
	return name, arguments
}

func openAIReplayBoundaryKind(_ agent.StepReplay, adapter openAIToolAdapter, ok bool) agent.ToolBoundaryKind {
	if ok && adapter.BoundaryKind != "" {
		return adapter.BoundaryKind
	}
	return agent.ToolBoundaryKindFunction
}

func openAIToolAdaptersByInternalName(tools []agent.ToolDefinition, caps openAIAdapterCapabilities) map[string]openAIToolAdapter {
	if len(tools) == 0 {
		return nil
	}
	adapters := buildOpenAIToolAdapters(tools, caps)
	byName := make(map[string]openAIToolAdapter, len(adapters))
	for _, adapter := range adapters {
		byName[adapter.InternalName] = adapter
	}
	return byName
}

func openAICustomToolArguments(tool agent.ToolDefinition, input string) string {
	return compactJSON(map[string]any{
		openAICustomToolInputField(tool): input,
	})
}

func openAICustomToolInputField(tool agent.ToolDefinition) string {
	if tool.Name == agent.ToolNameApplyPatch {
		return "patch"
	}
	return "input"
}

func buildOpenAIToolAdapters(tools []agent.ToolDefinition, caps openAIAdapterCapabilities) []openAIToolAdapter {
	if len(tools) == 0 {
		return nil
	}
	adapters := make([]openAIToolAdapter, 0, len(tools))
	for _, tool := range tools {
		adapter := openAIToolAdapter{
			InternalName: tool.Name,
			ExposedName:  tool.Name,
			BoundaryKind: agent.ToolBoundaryKindFunction,
			Tool:         tool,
		}
		preferredKind := tool.Interop.OpenAIPreferredKind
		fallbackKind := tool.Interop.OpenAIFallbackKind
		if fallbackKind == "" {
			fallbackKind = agent.ToolBoundaryKindFunction
		}
		fallbackName := strings.TrimSpace(tool.Interop.OpenAIFallbackToolName)
		if fallbackName == "" {
			fallbackName = tool.Name
		}
		switch preferredKind {
		case agent.ToolBoundaryKindCustom:
			if caps.SupportsCustomTools {
				adapter.BoundaryKind = preferredKind
			} else {
				adapter.BoundaryKind = fallbackKind
				adapter.ExposedName = fallbackName
			}
		case agent.ToolBoundaryKindLocalShell:
			if caps.SupportsLocalShell {
				adapter.BoundaryKind = preferredKind
			} else {
				adapter.BoundaryKind = fallbackKind
				adapter.ExposedName = fallbackName
			}
		case agent.ToolBoundaryKindWebSearch:
			if caps.SupportsHostedWebSearch {
				adapter.BoundaryKind = preferredKind
			} else {
				adapter.BoundaryKind = fallbackKind
				adapter.ExposedName = fallbackName
			}
		default:
			if preferredKind != "" {
				adapter.BoundaryKind = preferredKind
			}
		}
		adapters = append(adapters, adapter)
	}
	return adapters
}

func allowedOpenAIToolAdapter(allowedTools []agent.ToolDefinition, caps openAIAdapterCapabilities, exposedName string) (openAIToolAdapter, bool) {
	for _, adapter := range buildOpenAIToolAdapters(allowedTools, caps) {
		if adapter.ExposedName == exposedName {
			return adapter, true
		}
	}
	return openAIToolAdapter{}, false
}

func openAIToolDescription(tool agent.ToolDefinition) string {
	sections := make([]string, 0, 3)
	if description := strings.TrimSpace(tool.Description); description != "" {
		sections = append(sections, description)
	}
	if notes := strings.TrimSpace(tool.OutputNotes); notes != "" {
		sections = append(sections, "Output notes: "+notes)
	}
	if tags := strings.TrimSpace(strings.Join(tool.SafetyTags, ", ")); tags != "" {
		sections = append(sections, "Safety tags: "+tags+".")
	}
	return strings.Join(sections, "\n\n")
}
