package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/richardartoul/swarmd/pkg/agent"
	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

type MessageQueue struct {
	Store         *cpstore.Store
	NamespaceID   string
	AgentID       string
	PollInterval  time.Duration
	LeaseOwner    string
	LeaseDuration time.Duration
	SystemPrompt  string
	Logger        *RuntimeLogger
}

func (q *MessageQueue) Next(ctx context.Context) (agent.Trigger, error) {
	if q.Store == nil {
		return agent.Trigger{}, fmt.Errorf("server message queue requires a store")
	}
	pollInterval := q.PollInterval
	if pollInterval <= 0 {
		pollInterval = 250 * time.Millisecond
	}
	for {
		claimed, err := q.Store.ClaimNextMessage(ctx, cpstore.ClaimMessageParams{
			NamespaceID:   q.NamespaceID,
			AgentID:       q.AgentID,
			LeaseOwner:    q.LeaseOwner,
			LeaseDuration: q.LeaseDuration,
			SystemPrompt:  q.SystemPrompt,
		})
		if err == nil {
			q.Logger.LogRunStart(claimed)
			payload, err := cpstore.DecodeEnvelopeAny(claimed.Message.PayloadJSON)
			if err != nil {
				return agent.Trigger{}, fmt.Errorf("decode payload for mailbox message %q: %w", claimed.Message.ID, err)
			}
			metadata := map[string]any{
				metadataNamespaceID:  q.NamespaceID,
				metadataAgentID:      q.AgentID,
				metadataMessageID:    claimed.Message.ID,
				metadataThreadID:     claimed.Message.ThreadID,
				metadataRunID:        claimed.Run.ID,
				metadataAttemptCount: claimed.Message.AttemptCount,
				metadataMaxAttempts:  claimed.Message.MaxAttempts,
			}
			if claimed.Message.SenderAgentID != "" {
				metadata[metadataSenderAgentID] = claimed.Message.SenderAgentID
			}
			var decodedMetadata any
			if err := cpstore.DecodeEnvelopeInto(claimed.Message.MetadataJSON, &decodedMetadata); err == nil {
				if original, ok := decodedMetadata.(map[string]any); ok {
					for key, value := range original {
						metadata[key] = value
					}
				}
			}
			return agent.Trigger{
				ID:         claimed.Run.TriggerID,
				Kind:       claimed.Message.Kind,
				Payload:    payload,
				Metadata:   metadata,
				EnqueuedAt: claimed.Message.CreatedAt,
			}, nil
		}
		if !errors.Is(err, cpstore.ErrNoAvailableMessage) {
			return agent.Trigger{}, err
		}
		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return agent.Trigger{}, ctx.Err()
		case <-timer.C:
		}
	}
}
