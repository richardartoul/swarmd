package agent

import (
	"strings"
	"testing"
)

func TestParseStrictFinalResponseDecodesPayloads(t *testing.T) {
	t.Parallel()

	t.Run("string", func(t *testing.T) {
		t.Parallel()

		thought, value, err := ParseStrictFinalResponse(StrictFinalResponseExample("all work is complete", "done"))
		if err != nil {
			t.Fatalf("ParseStrictFinalResponse() error = %v", err)
		}
		if thought != "all work is complete" {
			t.Fatalf("thought = %q, want %q", thought, "all work is complete")
		}
		if value != "done" {
			t.Fatalf("value = %#v, want %q", value, "done")
		}
	})

	t.Run("object", func(t *testing.T) {
		t.Parallel()

		thought, value, err := ParseStrictFinalResponse(StrictFinalResponseExample("return the structured result", map[string]any{
			"status": "done",
			"count":  2,
		}))
		if err != nil {
			t.Fatalf("ParseStrictFinalResponse() error = %v", err)
		}
		if thought != "return the structured result" {
			t.Fatalf("thought = %q, want %q", thought, "return the structured result")
		}
		payload, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("value = %#v, want object", value)
		}
		if payload["status"] != "done" {
			t.Fatalf("value[status] = %#v, want %q", payload["status"], "done")
		}
		if payload["count"] != float64(2) {
			t.Fatalf("value[count] = %#v, want %v", payload["count"], 2)
		}
	})

	t.Run("empty thought", func(t *testing.T) {
		t.Parallel()

		thought, value, err := ParseStrictFinalResponse(`{"thought":"","result_json":"\"done\""}`)
		if err != nil {
			t.Fatalf("ParseStrictFinalResponse() error = %v", err)
		}
		if thought != "" {
			t.Fatalf("thought = %q, want empty thought", thought)
		}
		if value != "done" {
			t.Fatalf("value = %#v, want %q", value, "done")
		}
	})

	t.Run("missing thought", func(t *testing.T) {
		t.Parallel()

		thought, value, err := ParseStrictFinalResponse(`{"result_json":"\"done\""}`)
		if err != nil {
			t.Fatalf("ParseStrictFinalResponse() error = %v", err)
		}
		if thought != "" {
			t.Fatalf("thought = %q, want empty thought", thought)
		}
		if value != "done" {
			t.Fatalf("value = %#v, want %q", value, "done")
		}
	})
}

func TestParseStrictFinalResponseRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	for name, content := range map[string]string{
		"empty result_json":   `{"thought":"done","result_json":""}`,
		"invalid result_json": `{"thought":"done","result_json":"not-json"}`,
		"extra field":         `{"thought":"done","result_json":"\"ok\"","extra":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, _, err := ParseStrictFinalResponse(content)
			if err == nil {
				t.Fatal("ParseStrictFinalResponse() error = nil, want rejection")
			}
		})
	}
}

func TestStrictFinalResponseSchema(t *testing.T) {
	t.Parallel()

	schema := StrictFinalResponseSchema()
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema.properties = %#v, want object", schema["properties"])
	}
	if _, ok := properties["thought"]; !ok {
		t.Fatalf("schema.properties = %#v, want thought property", properties)
	}
	if thoughtSchema, ok := properties["thought"].(map[string]any); !ok {
		t.Fatalf("schema.properties[thought] = %#v, want object", properties["thought"])
	} else if !strings.Contains(thoughtSchema["description"].(string), "empty string") {
		t.Fatalf("schema.properties[thought].description = %#v, want empty-string guidance", thoughtSchema["description"])
	}
	if resultJSON, ok := properties["result_json"].(map[string]any); !ok {
		t.Fatalf("schema.properties[result_json] = %#v, want object", properties["result_json"])
	} else if !strings.Contains(resultJSON["description"].(string), "valid JSON") {
		t.Fatalf("schema.properties[result_json].description = %#v, want JSON guidance", resultJSON["description"])
	}
	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatalf("schema.required = %#v, want []string", schema["required"])
	}
	if len(required) != 2 || required[0] != "thought" || required[1] != "result_json" {
		t.Fatalf("schema.required = %#v, want [thought result_json]", required)
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("schema.additionalProperties = %#v, want false", schema["additionalProperties"])
	}
}
