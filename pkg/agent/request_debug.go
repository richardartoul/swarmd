// See LICENSE for licensing information

package agent

import (
	"io"
	"os"
	"strings"
	"sync"
)

// DebugPromptEnvVar enables writing the exact serialized model request payload to stderr.
const DebugPromptEnvVar = "SWARMD_DEBUG_PROMPT"

var (
	debugPromptMu     sync.Mutex
	debugPromptOutput io.Writer = os.Stderr
)

// MaybeWriteDebugPrompt writes the exact serialized model request payload to stderr
// when [DebugPromptEnvVar] is set to a non-empty value.
func MaybeWriteDebugPrompt(payload []byte) {
	if strings.TrimSpace(os.Getenv(DebugPromptEnvVar)) == "" || len(payload) == 0 {
		return
	}

	debugPromptMu.Lock()
	defer debugPromptMu.Unlock()

	_, _ = debugPromptOutput.Write(payload)
}
