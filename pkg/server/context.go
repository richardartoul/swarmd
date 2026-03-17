package server

import (
	"context"
	"fmt"

	"github.com/richardartoul/swarmd/pkg/agent"
)

const (
	metadataNamespaceID   = "server_namespace_id"
	metadataAgentID       = "server_agent_id"
	metadataMessageID     = "server_message_id"
	metadataThreadID      = "server_thread_id"
	metadataRunID         = "server_run_id"
	metadataSenderAgentID = "server_sender_agent_id"
	metadataAttemptCount  = "server_attempt_count"
	metadataMaxAttempts   = "server_max_attempts"
)

type TriggerContext struct {
	NamespaceID   string
	AgentID       string
	MessageID     string
	ThreadID      string
	RunID         string
	SenderAgentID string
}

func TriggerContextFromTrigger(trigger agent.Trigger) (TriggerContext, error) {
	return TriggerContextFromMetadata(trigger.Metadata)
}

func TriggerContextFromMetadata(metadata map[string]any) (TriggerContext, error) {
	if metadata == nil {
		return TriggerContext{}, fmt.Errorf("server trigger metadata missing")
	}
	ctx := TriggerContext{
		NamespaceID:   metadataString(metadata, metadataNamespaceID),
		AgentID:       metadataString(metadata, metadataAgentID),
		MessageID:     metadataString(metadata, metadataMessageID),
		ThreadID:      metadataString(metadata, metadataThreadID),
		RunID:         metadataString(metadata, metadataRunID),
		SenderAgentID: metadataString(metadata, metadataSenderAgentID),
	}
	if ctx.NamespaceID == "" || ctx.AgentID == "" || ctx.MessageID == "" || ctx.RunID == "" {
		return TriggerContext{}, fmt.Errorf(
			"server trigger metadata incomplete: namespace=%q agent=%q message=%q run=%q",
			ctx.NamespaceID,
			ctx.AgentID,
			ctx.MessageID,
			ctx.RunID,
		)
	}
	return ctx, nil
}

func triggerFromContext(ctx context.Context) (agent.Trigger, bool) {
	return agent.TriggerFromContext(ctx)
}

func metadataInt(metadata map[string]any, key string) int {
	value, _ := metadata[key]
	switch value := value.(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func metadataString(metadata map[string]any, key string) string {
	value, _ := metadata[key]
	switch value := value.(type) {
	case string:
		return value
	default:
		return ""
	}
}
