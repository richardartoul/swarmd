package server

import (
	"encoding/json"
	"fmt"

	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

func RenderEnvelope(data string) string {
	value, err := cpstore.DecodeEnvelopeAny(data)
	if err != nil {
		return data
	}
	return RenderValue(value)
}

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
