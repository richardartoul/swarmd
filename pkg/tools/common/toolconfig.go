package common

import (
	"bytes"
	"encoding/json"
	"fmt"
)

func ValidateNoToolConfig(toolID string, raw map[string]any) error {
	if len(raw) == 0 {
		return nil
	}
	return fmt.Errorf("tool %q does not accept config", toolID)
}

func DecodeToolConfig[T any](raw map[string]any) (T, error) {
	var config T
	if len(raw) == 0 {
		return config, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return config, fmt.Errorf("marshal tool config: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return config, fmt.Errorf("decode tool config: %w", err)
	}
	return config, nil
}

func MarshalToolOutput(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal tool output: %w", err)
	}
	return string(encoded), nil
}
