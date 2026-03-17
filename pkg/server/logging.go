package server

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/richardartoul/swarmd/pkg/agent"
	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

type RuntimeLogger struct {
	stdout io.Writer
	stderr io.Writer
	mu     sync.Mutex
}

func NewRuntimeLogger(stdout, stderr io.Writer) *RuntimeLogger {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	return &RuntimeLogger{
		stdout: stdout,
		stderr: stderr,
	}
}

func (l *RuntimeLogger) LogRunStart(claimed cpstore.ClaimedMailboxMessage) {
	if l == nil {
		return
	}
	l.printf(
		l.stdout,
		"run> %s/%s run=%s message=%s kind=%s attempt=%d started\n",
		claimed.Message.NamespaceID,
		claimed.Message.RecipientAgentID,
		claimed.Run.ID,
		claimed.Message.ID,
		claimed.Message.Kind,
		claimed.Message.AttemptCount,
	)
}

func (l *RuntimeLogger) LogResult(triggerCtx TriggerContext, result agent.Result) {
	if l == nil {
		return
	}
	writer := l.stdout
	if result.Status != agent.ResultStatusFinished {
		writer = l.stderr
	}
	line := fmt.Sprintf(
		"run> %s/%s run=%s finished status=%s dur=%s steps=%d",
		triggerCtx.NamespaceID,
		triggerCtx.AgentID,
		triggerCtx.RunID,
		result.Status,
		result.Duration.Round(10*time.Millisecond),
		len(result.Steps),
	)
	if result.Error != "" {
		line += fmt.Sprintf(" error=%q", summarizeLogText(result.Error, 200))
	}
	l.printf(writer, "%s\n", line)
}

func (l *RuntimeLogger) LogAgentCommand(triggerCtx TriggerContext, level, message string) {
	if l == nil {
		return
	}
	writer := l.stdout
	switch level {
	case "warn", "error":
		writer = l.stderr
	}
	line := fmt.Sprintf(
		"agent_log> %s/%s run=%s message=%s level=%s msg=%q",
		triggerCtx.NamespaceID,
		triggerCtx.AgentID,
		triggerCtx.RunID,
		triggerCtx.MessageID,
		level,
		summarizeLogText(message, 400),
	)
	l.printf(writer, "%s\n", line)
}

func (l *RuntimeLogger) printf(w io.Writer, format string, args ...any) {
	if l == nil || w == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = fmt.Fprintf(w, format, args...)
}

func summarizeLogText(text string, max int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if max > 0 && len(text) > max {
		return text[:max-3] + "..."
	}
	return text
}
