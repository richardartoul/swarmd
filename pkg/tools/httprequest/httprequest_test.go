package httprequest

import (
	"reflect"
	"strings"
	"testing"

	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
)

func TestDefinitionUsesHeaderArraySchema(t *testing.T) {
	t.Parallel()

	definition := plugin{}.Definition()
	properties, ok := definition.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("definition.Parameters[properties] = %#v, want object", definition.Parameters["properties"])
	}
	headers, ok := properties["headers"].(map[string]any)
	if !ok {
		t.Fatalf("headers schema = %#v, want object", properties["headers"])
	}
	if got := headers["type"]; got != "array" {
		t.Fatalf("headers schema type = %#v, want %q", got, "array")
	}
	items, ok := headers["items"].(map[string]any)
	if !ok {
		t.Fatalf("headers.items = %#v, want object", headers["items"])
	}
	if got := items["type"]; got != "object" {
		t.Fatalf("headers.items.type = %#v, want %q", got, "object")
	}
	if got := items["additionalProperties"]; got != false {
		t.Fatalf("headers.items.additionalProperties = %#v, want false", got)
	}
	itemProperties, ok := items["properties"].(map[string]any)
	if !ok {
		t.Fatalf("headers.items.properties = %#v, want object", items["properties"])
	}
	if _, ok := itemProperties["name"]; !ok {
		t.Fatalf("headers.items.properties = %#v, want name field", itemProperties)
	}
	if _, ok := itemProperties["value"]; !ok {
		t.Fatalf("headers.items.properties = %#v, want value field", itemProperties)
	}
	required, ok := items["required"].([]string)
	if !ok {
		t.Fatalf("headers.items.required = %#v, want []string", items["required"])
	}
	if want := []string{"name", "value"}; !reflect.DeepEqual(required, want) {
		t.Fatalf("headers.items.required = %#v, want %#v", required, want)
	}
	if len(definition.Examples) < 2 || !strings.Contains(definition.Examples[1], `"headers":[{"name":"Content-Type","value":"application/json"}]`) {
		t.Fatalf("definition.Examples = %#v, want array-based headers example", definition.Examples)
	}
}

func TestDecodeToolInputSupportsLegacyHeaderMap(t *testing.T) {
	t.Parallel()

	args, err := toolscore.DecodeToolInput[args](`{"url":"https://example.com","headers":{"Authorization":"Bearer secret","Content-Type":"application/json"}}`)
	if err != nil {
		t.Fatalf("DecodeToolInput() error = %v", err)
	}
	got := make(map[string]string, len(args.Headers))
	for _, header := range args.Headers {
		got[header.Name] = header.Value
	}
	want := map[string]string{
		"Authorization": "Bearer secret",
		"Content-Type":  "application/json",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded headers = %#v, want %#v", got, want)
	}
}
