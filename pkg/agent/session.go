// See LICENSE for licensing information

package agent

import (
	"context"
	"fmt"
)

// Session keeps one long-lived conversation around an existing [Agent].
// It is intended for interactive callers such as agentrepl and is not safe for
// concurrent use.
type Session struct {
	agent         *Agent
	conversation  []Message
	steps         []Step
	nextStepIndex int
}

// NewSession wraps one [Agent] in a long-lived conversational session.
func NewSession(agent *Agent) *Session {
	return &Session{
		agent:         agent,
		nextStepIndex: 1,
	}
}

// RunTrigger handles one user turn within the current session while preserving
// prior user/assistant turns and replayable steps for later prompts.
func (s *Session) RunTrigger(ctx context.Context, trigger Trigger) (Result, error) {
	if s == nil || s.agent == nil {
		return Result{}, fmt.Errorf("agent session requires a non-nil agent")
	}

	prompt, err := triggerPrompt(trigger)
	if err != nil {
		return Result{}, err
	}
	userTurn := Message{
		Role:    MessageRoleUser,
		Content: prompt,
	}

	result, runErr := s.agent.runTurn(ctx, turnRunInput{
		Trigger:       trigger,
		PriorSteps:    s.steps,
		NextStepIndex: s.nextStepIndex,
		ResetRunner:   !s.agent.preserveState,
		RequestContext: newSessionDriverRequestContext(
			s.conversation,
		),
	})

	s.conversation = append(s.conversation, userTurn)
	if assistantTurn, ok := sessionAssistantTurn(result); ok {
		s.conversation = append(s.conversation, assistantTurn)
	}
	if len(result.Steps) > 0 {
		s.steps = append(s.steps, cloneSteps(result.Steps)...)
		s.nextStepIndex += len(result.Steps)
	}

	if runErr != nil {
		return result, runErr
	}
	if err := s.agent.handleResult(ctx, result); err != nil {
		return result, err
	}
	return result, nil
}

func sessionAssistantTurn(result Result) (Message, bool) {
	if result.Status != ResultStatusFinished {
		return Message{}, false
	}
	return Message{
		Role:    MessageRoleAssistant,
		Content: RenderResultValue(result.Value),
	}, true
}
