package server

import (
	"strings"
	"testing"
)

const (
	testServerLogToolID    = "server_log"
	testSlackPostToolID    = "slack_post"
	testSlackHistoryToolID = "slack_channel_history"
	testDatadogReadToolID  = "datadog_read"
	testSlackUserTokenEnv  = "SLACK_USER_TOKEN"
	testDatadogAPIKeyEnv   = "DD_API_KEY"
	testDatadogAppKeyEnv   = "DD_APP_KEY"
)

func TestValidateReferencedToolEnvRejectsMissingRequiredVars(t *testing.T) {
	t.Parallel()

	err := ValidateReferencedToolEnv([]AgentSpec{{
		NamespaceID: "default",
		AgentID:     "worker",
		Tools: []AgentToolSpec{{
			ID: testSlackPostToolID,
		}},
	}}, func(string) string {
		return ""
	})
	if err == nil {
		t.Fatal("ValidateReferencedToolEnv() error = nil, want missing env error")
	}
	if !strings.Contains(err.Error(), testSlackUserTokenEnv) || !strings.Contains(err.Error(), testSlackPostToolID) {
		t.Fatalf("ValidateReferencedToolEnv() error = %v, want slack tool env requirement", err)
	}
}

func TestValidateReferencedToolEnvRejectsMissingSlackHistoryVars(t *testing.T) {
	t.Parallel()

	err := ValidateReferencedToolEnv([]AgentSpec{{
		NamespaceID: "default",
		AgentID:     "worker",
		Tools: []AgentToolSpec{{
			ID: testSlackHistoryToolID,
		}},
	}}, func(string) string {
		return ""
	})
	if err == nil {
		t.Fatal("ValidateReferencedToolEnv() error = nil, want missing env error")
	}
	if !strings.Contains(err.Error(), testSlackUserTokenEnv) || !strings.Contains(err.Error(), testSlackHistoryToolID) {
		t.Fatalf("ValidateReferencedToolEnv() error = %v, want slack history tool env requirement", err)
	}
}

func TestValidateReferencedToolEnvRejectsMissingDatadogVars(t *testing.T) {
	t.Parallel()

	err := ValidateReferencedToolEnv([]AgentSpec{{
		NamespaceID: "default",
		AgentID:     "worker",
		Tools: []AgentToolSpec{{
			ID: testDatadogReadToolID,
		}},
	}}, func(key string) string {
		if key == testDatadogAPIKeyEnv {
			return "api-key"
		}
		return ""
	})
	if err == nil {
		t.Fatal("ValidateReferencedToolEnv() error = nil, want missing Datadog env error")
	}
	if !strings.Contains(err.Error(), testDatadogAppKeyEnv) || !strings.Contains(err.Error(), testDatadogReadToolID) {
		t.Fatalf("ValidateReferencedToolEnv() error = %v, want Datadog env requirement", err)
	}
}

func TestValidateReferencedToolEnvIgnoresUnreferencedTools(t *testing.T) {
	t.Parallel()

	err := ValidateReferencedToolEnv([]AgentSpec{{
		NamespaceID: "default",
		AgentID:     "worker",
		Tools: []AgentToolSpec{{
			ID: testServerLogToolID,
		}},
	}}, func(string) string {
		return ""
	})
	if err != nil {
		t.Fatalf("ValidateReferencedToolEnv() error = %v, want nil for unreferenced env requirements", err)
	}
}
