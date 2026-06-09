package server

import (
	"encoding/json"
	"fmt"

	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

// RenderEnvelope renders a stored JSON envelope as display text.
func RenderEnvelope(data string) string {
	value, err := cpstore.DecodeEnvelopeAny(data)
	if err != nil {
		return data
	}
	return RenderValue(value)
}

// RenderValue renders an arbitrary value as display text.
func RenderValue(value any) string {
	switch value := value.(type) {
	case nil:
		return ""
	case string:
		return value
	default:
		rendered, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return fmt.Sprint(value)
		}
		return string(rendered)
	}
}
