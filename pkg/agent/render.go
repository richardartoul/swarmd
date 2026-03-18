// See LICENSE for licensing information

package agent

import (
	"encoding/json"
	"fmt"
)

// RenderResultValue formats one finished result value for user-facing output.
// Nil results render as "done" to match agentrepl's existing finish UX.
func RenderResultValue(value any) string {
	if value == nil {
		return "done"
	}
	switch value := value.(type) {
	case string:
		return value
	default:
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return fmt.Sprint(value)
		}
		return string(data)
	}
}
