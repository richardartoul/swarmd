package store

import (
	"encoding/json"
	"fmt"
)

const envelopeVersion = 1

func MarshalEnvelope(kind string, value any) (string, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal envelope body: %w", err)
	}
	data, err := json.Marshal(JSONEnvelope{
		Version: envelopeVersion,
		Type:    kind,
		Body:    body,
	})
	if err != nil {
		return "", fmt.Errorf("marshal envelope: %w", err)
	}
	return string(data), nil
}

func MarshalOptionalEnvelope(kind string, value any) (string, error) {
	if value == nil {
		return "", nil
	}
	return MarshalEnvelope(kind, value)
}

func DecodeEnvelopeAny(data string) (any, error) {
	if data == "" {
		return nil, nil
	}
	var env JSONEnvelope
	if err := json.Unmarshal([]byte(data), &env); err != nil {
		return nil, fmt.Errorf("decode envelope: %w", err)
	}
	if env.Version != envelopeVersion {
		return nil, fmt.Errorf("unsupported envelope version %d", env.Version)
	}
	if len(env.Body) == 0 {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal(env.Body, &value); err != nil {
		return nil, fmt.Errorf("decode envelope body: %w", err)
	}
	return value, nil
}

func DecodeEnvelopeInto(data string, dst any) error {
	if data == "" {
		return nil
	}
	var env JSONEnvelope
	if err := json.Unmarshal([]byte(data), &env); err != nil {
		return fmt.Errorf("decode envelope: %w", err)
	}
	if env.Version != envelopeVersion {
		return fmt.Errorf("unsupported envelope version %d", env.Version)
	}
	if len(env.Body) == 0 {
		return nil
	}
	if err := json.Unmarshal(env.Body, dst); err != nil {
		return fmt.Errorf("decode envelope body: %w", err)
	}
	return nil
}
