package openai

import (
	"encoding/json"
	"testing"

	"github.com/richardartoul/swarmd/pkg/agent"
)

func TestBuildChatCompletionToolsUsesEmptyArrayForNestedRequired(t *testing.T) {
	t.Parallel()

	allTools, err := agent.ResolveToolDefinitions(nil, true)
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

	tools := buildChatCompletionTools([]agent.ToolDefinition{readFileTool}, openAIAdapterCapabilities{})
	if len(tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(tools))
	}

	encoded, err := json.Marshal(tools[0].Function.Parameters)
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
