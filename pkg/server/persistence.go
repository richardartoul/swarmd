package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/richardartoul/swarmd/pkg/agent"
	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
)

type StepPersister struct {
	Store  *cpstore.Store
	Logger *RuntimeLogger
}

func (p StepPersister) HandleStep(ctx context.Context, trigger agent.Trigger, step agent.Step) error {
	if p.Store == nil {
		return fmt.Errorf("server step persister requires a store")
	}
	triggerCtx, err := TriggerContextFromTrigger(trigger)
	if err != nil {
		return err
	}
	if err := p.Store.RecordStep(ctx, cpstore.StepRecord{
		NamespaceID:           triggerCtx.NamespaceID,
		RunID:                 triggerCtx.RunID,
		MessageID:             triggerCtx.MessageID,
		AgentID:               triggerCtx.AgentID,
		StepIndex:             step.Index,
		StepType:              string(step.Type),
		Thought:               step.Thought,
		Shell:                 step.Shell,
		ActionName:            step.ActionName,
		ActionToolKind:        string(step.ActionToolKind),
		ActionInput:           step.ActionInput,
		ActionOutput:          step.ActionOutput,
		ActionOutputTruncated: step.ActionOutputTruncated,
		UsageCachedTokens:     step.Usage.CachedTokens,
		CWDBefore:             step.CWDBefore,
		CWDAfter:              step.CWDAfter,
		Stdout:                step.Stdout,
		Stderr:                step.Stderr,
		StdoutTruncated:       step.StdoutTruncated,
		StderrTruncated:       step.StderrTruncated,
		StartedAt:             step.StartedAt,
		FinishedAt:            step.FinishedAt,
		Duration:              step.Duration,
		Status:                string(step.Status),
		ExitStatus:            step.ExitStatus,
		Error:                 step.Error,
	}); err != nil {
		return err
	}
	return nil
}

type ResultPersister struct {
	Store            *cpstore.Store
	RetryDelay       time.Duration
	Logger           *RuntimeLogger
	AllowMessageSend bool
}

func (p ResultPersister) HandleResult(ctx context.Context, result agent.Result) error {
	if p.Store == nil {
		return fmt.Errorf("server result persister requires a store")
	}
	triggerCtx, err := TriggerContextFromTrigger(result.Trigger)
	if err != nil {
		return err
	}
	retryDelay := p.RetryDelay
	if retryDelay <= 0 {
		retryDelay = 30 * time.Second
	}

	var retryAt *time.Time
	deadLetterReason := ""
	if result.Status != agent.ResultStatusFinished {
		attemptCount := metadataInt(result.Trigger.Metadata, metadataAttemptCount)
		maxAttempts := metadataInt(result.Trigger.Metadata, metadataMaxAttempts)
		if maxAttempts > 0 && attemptCount >= maxAttempts {
			deadLetterReason = fmt.Sprintf("message exhausted retries after result status %s", result.Status)
		} else {
			next := time.Now().UTC().Add(retryDelay)
			retryAt = &next
		}
	}

	outbox, err := extractOutbox(result.Value, triggerCtx)
	if err != nil {
		return err
	}
	if result.Status != agent.ResultStatusFinished {
		outbox = nil
	}
	if len(outbox) > 0 && !p.AllowMessageSend {
		return fmt.Errorf(
			"server agent %q/%q attempted to send outbox messages without %q capability",
			triggerCtx.NamespaceID,
			triggerCtx.AgentID,
			capabilityAllowMessageSend,
		)
	}

	if err := p.Store.CompleteRun(ctx, cpstore.CompleteRunParams{
		NamespaceID:       triggerCtx.NamespaceID,
		RunID:             triggerCtx.RunID,
		MessageID:         triggerCtx.MessageID,
		Status:            string(result.Status),
		FinishedAt:        result.FinishedAt,
		Duration:          result.Duration,
		CWD:               result.CWD,
		UsageCachedTokens: result.Usage.CachedTokens,
		Value:             result.Value,
		Error:             result.Error,
		RetryAt:           retryAt,
		DeadLetterReason:  deadLetterReason,
		Outbox:            outbox,
	}); err != nil {
		return err
	}
	p.Logger.LogResult(triggerCtx, result)
	return nil
}

type WorkerResultEnvelope struct {
	Reply  any                   `json:"reply,omitempty"`
	Outbox []WorkerOutboxMessage `json:"outbox,omitempty"`
}

type WorkerOutboxMessage struct {
	RecipientAgentID string `json:"recipient_agent_id"`
	ThreadID         string `json:"thread_id,omitempty"`
	Kind             string `json:"kind,omitempty"`
	Payload          any    `json:"payload"`
	Metadata         any    `json:"metadata,omitempty"`
	AvailableAt      string `json:"available_at,omitempty"`
	MaxAttempts      int    `json:"max_attempts,omitempty"`
}

func extractOutbox(value any, triggerCtx TriggerContext) ([]cpstore.CreateMailboxMessageParams, error) {
	if value == nil {
		return nil, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal worker result envelope: %w", err)
	}
	var envelope WorkerResultEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, nil
	}
	if len(envelope.Outbox) == 0 {
		return nil, nil
	}
	outbox := make([]cpstore.CreateMailboxMessageParams, 0, len(envelope.Outbox))
	for index, message := range envelope.Outbox {
		if message.RecipientAgentID == "" {
			return nil, fmt.Errorf("worker result outbox entry %d is missing recipient_agent_id", index)
		}
		var availableAt time.Time
		if message.AvailableAt != "" {
			availableAt, err = time.Parse(time.RFC3339Nano, message.AvailableAt)
			if err != nil {
				return nil, fmt.Errorf("parse worker outbox available_at %q: %w", message.AvailableAt, err)
			}
		}
		outbox = append(outbox, cpstore.CreateMailboxMessageParams{
			NamespaceID:      triggerCtx.NamespaceID,
			ThreadID:         toolscommon.FirstNonEmptyString(message.ThreadID, triggerCtx.ThreadID),
			SenderAgentID:    triggerCtx.AgentID,
			RecipientAgentID: message.RecipientAgentID,
			Kind:             toolscommon.FirstNonEmptyString(message.Kind, "agent.message"),
			Payload:          message.Payload,
			Metadata:         message.Metadata,
			AvailableAt:      availableAt,
			MaxAttempts:      message.MaxAttempts,
		})
	}
	return outbox, nil
}
