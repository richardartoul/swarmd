// See LICENSE for licensing information

package agent

import (
	"io"
	"os"
	"strings"
	"sync"
)

const (
	// DebugPromptEnvVar enables writing the exact serialized model request payload to stderr.
	DebugPromptEnvVar = "SWARMD_DEBUG_PROMPT"
	// DebugResponseEnvVar enables writing the exact raw model response payload to stderr.
	DebugResponseEnvVar = "SWARMD_DEBUG_RESPONSE"
)

var (
	debugPayloadMu      sync.Mutex
	debugPromptOutput   io.Writer = os.Stderr
	debugResponseOutput io.Writer = os.Stderr
)

// MaybeWriteDebugPrompt writes the exact serialized model request payload to stderr
// when [DebugPromptEnvVar] is set to a non-empty value.
func MaybeWriteDebugPrompt(payload []byte) {
	maybeWriteDebugPayload(DebugPromptEnvVar, debugPromptOutput, payload)
}

// MaybeWriteDebugResponse writes the exact raw model response payload to stderr
// when [DebugResponseEnvVar] is set to a non-empty value.
func MaybeWriteDebugResponse(payload []byte) {
	maybeWriteDebugPayload(DebugResponseEnvVar, debugResponseOutput, payload)
}

func maybeWriteDebugPayload(envVar string, output io.Writer, payload []byte) {
	if strings.TrimSpace(os.Getenv(envVar)) == "" || len(payload) == 0 {
		return
	}

	debugPayloadMu.Lock()
	defer debugPayloadMu.Unlock()

	_, _ = output.Write(payload)
}
