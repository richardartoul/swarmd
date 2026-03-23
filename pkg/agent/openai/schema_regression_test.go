package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/richardartoul/swarmd/pkg/agent"
	"github.com/richardartoul/swarmd/pkg/sh/interp"
)

func TestBuildResponsesToolsUsesEmptyArrayForNestedRequired(t *testing.T) {
	t.Parallel()

	allTools, err := agent.ResolveToolDefinitions(nil, []interp.HostMatcher{{Glob: "*"}})
	if err != nil {
		t.Fatalf("ResolveToolDefinitions() error = %v", err)
	}

	var readFileTool agent.ToolDefinition
	found := false
	for _, tool := range allTools {
		if tool.Name == agent.ToolNameReadFile {
			readFileTool = tool
			found = true
			break
		}
	}
	if !found {
		t.Fatal("read_file tool definition not found")
	}

	tools := buildResponsesTools([]agent.ToolDefinition{readFileTool}, openAIAdapterCapabilities{})
	if len(tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(tools))
	}

	encoded, err := json.Marshal(tools[0].Parameters)
	if err != nil {
		t.Fatalf("json.Marshal(parameters) error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(parameters) error = %v", err)
	}

	properties, ok := decoded["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v, want object", decoded["properties"])
	}
	indentation, ok := properties["indentation"].(map[string]any)
	if !ok {
		t.Fatalf("indentation = %#v, want object", properties["indentation"])
	}
	required, ok := indentation["required"].([]any)
	if !ok {
		t.Fatalf("indentation.required = %#v, want array", indentation["required"])
	}
	if len(required) != 0 {
		t.Fatalf("len(indentation.required) = %d, want 0", len(required))
	}
}

func TestBuildResponsesToolsStripsTopLevelSchemaCombinators(t *testing.T) {
	t.Parallel()

	parameters := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path":    map[string]any{"type": "string"},
			"image_base64": map[string]any{"type": "string"},
			"image_url":    map[string]any{"type": "string"},
			"media_type":   map[string]any{"type": "string"},
		},
		"required":             []string{},
		"additionalProperties": false,
		"oneOf": []map[string]any{
			{"required": []string{"file_path"}},
			{"required": []string{"image_base64"}},
			{"required": []string{"image_url"}},
		},
	}

	tools := buildResponsesTools([]agent.ToolDefinition{{
		Name:       agent.ToolNameDescribeImage,
		Kind:       agent.ToolKindFunction,
		Parameters: parameters,
	}}, openAIAdapterCapabilities{})
	if len(tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(tools))
	}
	if _, ok := tools[0].Parameters["oneOf"]; ok {
		t.Fatalf("parameters = %#v, want top-level oneOf removed for OpenAI compatibility", tools[0].Parameters)
	}
	description, ok := tools[0].Parameters["description"].(string)
	if !ok {
		t.Fatalf("parameters description = %#v, want compatibility note string", tools[0].Parameters["description"])
	}
	if !strings.Contains(description, "top-level oneOf constraint") {
		t.Fatalf("parameters description = %q, want original top-level oneOf note", description)
	}
	if _, ok := parameters["oneOf"]; !ok {
		t.Fatalf("original parameters = %#v, want source schema left untouched", parameters)
	}
}
