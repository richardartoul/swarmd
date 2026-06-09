package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/richardartoul/swarmd/pkg/agent"
	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
)

// StepPersister records each completed agent step on the run's step log.
type StepPersister struct {
	Store  *cpstore.Store
	Logger *RuntimeLogger
}

// HandleStep implements [agent.StepHandler].
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
		Usage:                 step.Usage,
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

// ResultPersister completes the run and its mailbox message when a trigger
// finishes, applying retry, dead-letter, and outbox policy.
type ResultPersister struct {
	Store            *cpstore.Store
	RetryDelay       time.Duration
	Logger           *RuntimeLogger
	AllowMessageSend bool
}

// HandleResult implements [agent.ResultHandler]. Outbox violations in
// agent-produced output are recorded and dead-lettered, never returned:
// failing here would leave the message leased and re-run the agent.
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

	// The outbox is agent-produced output and therefore untrusted. Rejecting
	// it must never fail this handler: an error here would kill the worker
	// loop before the run is marked complete, leaving the message leased and
	// scheduled for another full (paid) agent run after lease expiry.
	// Instead, drop the outbox, dead-letter the message, and record why.
	resultError := result.Error
	outbox, outboxErr := validateOutbox(result, triggerCtx, p.AllowMessageSend)
	if outboxErr == nil {
		var infraErr error
		outboxErr, infraErr = p.validateOutboxRecipients(ctx, outbox)
		if infraErr != nil {
			return infraErr
		}
	}
	if outboxErr != nil {
		outbox = nil
		retryAt = nil
		deadLetterReason = fmt.Sprintf("outbox rejected: %v", outboxErr)
		resultError = joinErrorText(resultError, deadLetterReason)
	}

	if err := p.Store.CompleteRun(ctx, cpstore.CompleteRunParams{
		NamespaceID:      triggerCtx.NamespaceID,
		RunID:            triggerCtx.RunID,
		MessageID:        triggerCtx.MessageID,
		Status:           string(result.Status),
		FinishedAt:       result.FinishedAt,
		Duration:         result.Duration,
		CWD:              result.CWD,
		Usage:            result.Usage,
		FinishThought:    result.FinishThought,
		Value:            result.Value,
		Error:            resultError,
		RetryAt:          retryAt,
		DeadLetterReason: deadLetterReason,
		Outbox:           outbox,
	}); err != nil {
		return err
	}
	p.Logger.LogResult(triggerCtx, result)
	return nil
}

// validateOutbox extracts and authorizes outbox messages from a finished run.
// Errors describe agent-side policy violations, not infrastructure failures.
func validateOutbox(result agent.Result, triggerCtx TriggerContext, allowMessageSend bool) ([]cpstore.CreateMailboxMessageParams, error) {
	if result.Status != agent.ResultStatusFinished {
		return nil, nil
	}
	outbox, err := extractOutbox(result.Value, triggerCtx)
	if err != nil {
		return nil, err
	}
	if len(outbox) > 0 && !allowMessageSend {
		return nil, fmt.Errorf(
			"agent %q/%q attempted to send outbox messages without %q capability",
			triggerCtx.NamespaceID,
			triggerCtx.AgentID,
			capabilityAllowMessageSend,
		)
	}
	return outbox, nil
}

// validateOutboxRecipients confirms every outbox recipient exists in its
// target namespace. Checking up front turns a bad recipient into a recordable
// policy violation instead of a foreign-key failure inside CompleteRun that
// would abort the whole completion transaction.
//
// The first return value reports agent-side policy violations; the second
// reports infrastructure failures that should propagate to the caller.
func (p ResultPersister) validateOutboxRecipients(ctx context.Context, outbox []cpstore.CreateMailboxMessageParams) (error, error) {
	for _, message := range outbox {
		_, err := p.Store.GetAgent(ctx, message.NamespaceID, message.RecipientAgentID)
		if errors.Is(err, cpstore.ErrNotFound) {
			return fmt.Errorf(
				"outbox recipient %q does not exist in namespace %q",
				message.RecipientAgentID,
				message.NamespaceID,
			), nil
		}
		if err != nil {
			return nil, fmt.Errorf("verify outbox recipient %q: %w", message.RecipientAgentID, err)
		}
	}
	return nil, nil
}

func joinErrorText(existing, addition string) string {
	if existing == "" {
		return addition
	}
	if addition == "" {
		return existing
	}
	return existing + "; " + addition
}

// WorkerResultEnvelope is the optional structured result a worker agent may
// return: a user-facing reply plus outbox messages to other agents.
type WorkerResultEnvelope struct {
	Reply  any                   `json:"reply,omitempty"`
	Outbox []WorkerOutboxMessage `json:"outbox,omitempty"`
}

// WorkerOutboxMessage is one agent-requested mailbox delivery.
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
