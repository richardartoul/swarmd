package server

import (
	"strings"
	"time"

	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

func normalizedAgentName(params cpstore.CreateAgentParams) string {
	value := strings.TrimSpace(params.Name)
	if value == "" {
		return params.AgentID
	}
	return value
}

func normalizedAgentRole(params cpstore.CreateAgentParams) cpstore.AgentRole {
	if params.Role == "" {
		return cpstore.AgentRoleWorker
	}
	return params.Role
}

func normalizedDesiredState(params cpstore.CreateAgentParams) cpstore.AgentDesiredState {
	if params.DesiredState == "" {
		return cpstore.AgentDesiredStateRunning
	}
	return params.DesiredState
}

func normalizedModelProvider(params cpstore.CreateAgentParams) string {
	value := strings.TrimSpace(params.ModelProvider)
	if value == "" {
		return "openai"
	}
	return value
}

func normalizedMaxSteps(params cpstore.CreateAgentParams) int {
	if params.MaxSteps == 0 {
		return 32
	}
	return params.MaxSteps
}

func normalizedStepTimeout(params cpstore.CreateAgentParams) time.Duration {
	if params.StepTimeout == 0 {
		return 30 * time.Second
	}
	return params.StepTimeout
}

func normalizedMaxOutputBytes(params cpstore.CreateAgentParams) int {
	if params.MaxOutputBytes == 0 {
		return 64 << 10
	}
	return params.MaxOutputBytes
}

func normalizedLeaseDuration(params cpstore.CreateAgentParams) time.Duration {
	if params.LeaseDuration == 0 {
		return 5 * time.Minute
	}
	return params.LeaseDuration
}

func normalizedRetryDelay(params cpstore.CreateAgentParams) time.Duration {
	if params.RetryDelay == 0 {
		return 30 * time.Second
	}
	return params.RetryDelay
}

func normalizedMaxAttempts(params cpstore.CreateAgentParams) int {
	if params.MaxAttempts == 0 {
		return 5
	}
	return params.MaxAttempts
}

func normalizedSystemPrompt(prompt string) string {
	value := strings.TrimSpace(prompt)
	if value == "" {
		return "You are an agent managed by the SQLite server."
	}
	return value
}
