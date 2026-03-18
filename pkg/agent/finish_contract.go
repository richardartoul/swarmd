// See LICENSE for licensing information

package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type strictFinalEnvelope struct {
	Thought    string `json:"thought"`
	ResultJSON string `json:"result_json"`
}

// StrictFinalResponseSchema returns the canonical provider-facing finish schema.
func StrictFinalResponseSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"thought": map[string]any{
				"type": "string",
			},
			"result_json": map[string]any{
				"type":        "string",
				"description": "Encode the final user-facing payload as valid JSON inside this string.",
			},
		},
		"required":             []string{"thought", "result_json"},
		"additionalProperties": false,
	}
}

// StrictFinalResponseShape returns the canonical prompt-facing JSON shape.
func StrictFinalResponseShape() string {
	return `{"thought":"<brief completion reason>","result_json":"<valid JSON string>"}`
}

// StrictFinalResponseExample returns one concrete prompt example.
func StrictFinalResponseExample(thought string, result any) string {
	envelope := strictFinalEnvelope{
		Thought:    strings.TrimSpace(thought),
		ResultJSON: mustCompactJSON(result),
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return StrictFinalResponseShape()
	}
	return string(data)
}

// ParseStrictFinalResponse decodes one strict final response envelope and
// returns the finish thought plus the decoded payload value.
func ParseStrictFinalResponse(content string) (string, any, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return "", nil, fmt.Errorf("strict final response content was empty")
	}

	var envelope strictFinalEnvelope
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return "", nil, fmt.Errorf("could not decode strict final response: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return "", nil, fmt.Errorf("strict final response must contain exactly one JSON object")
		}
		return "", nil, fmt.Errorf("could not validate strict final response tail: %w", err)
	}

	thought := strings.TrimSpace(envelope.Thought)
	if thought == "" {
		return "", nil, fmt.Errorf(`strict final response must include non-empty "thought"`)
	}
	encoded := strings.TrimSpace(envelope.ResultJSON)
	if encoded == "" {
		return "", nil, fmt.Errorf(`strict final response must include non-empty "result_json"`)
	}

	var value any
	if err := json.Unmarshal([]byte(encoded), &value); err != nil {
		return "", nil, fmt.Errorf(`strict final response contains invalid JSON in "result_json": %w`, err)
	}
	return thought, value, nil
}
