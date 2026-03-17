package server

import (
	"context"
	"testing"

	agentanthropic "github.com/richardartoul/swarmd/pkg/agent/anthropic"
	agentopenai "github.com/richardartoul/swarmd/pkg/agent/openai"
	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

func TestDefaultWorkerPromptCacheSettingsUsesStableDefaults(t *testing.T) {
	t.Parallel()

	key, retention := defaultWorkerPromptCacheSettings(cpstore.RunnableAgent{
		AgentRecord: cpstore.AgentRecord{
			NamespaceID: "default",
			ID:          "worker-a",
			ModelName:   "gpt-5.4-xhigh",
		},
	})

	if key != "server:default:worker-a" {
		t.Fatalf("prompt cache key = %q, want %q", key, "server:default:worker-a")
	}
	if retention != "24h" {
		t.Fatalf("prompt cache retention = %q, want %q", retention, "24h")
	}
}

func TestDefaultWorkerPromptCacheSettingsSkipsCustomBaseURL(t *testing.T) {
	t.Parallel()

	key, retention := defaultWorkerPromptCacheSettings(cpstore.RunnableAgent{
		AgentRecord: cpstore.AgentRecord{
			NamespaceID:  "default",
			ID:           "worker-a",
			ModelName:    "gpt-5.4",
			ModelBaseURL: "http://localhost:11434/v1",
		},
	})

	if key != "" {
		t.Fatalf("prompt cache key = %q, want empty for custom base URL", key)
	}
	if retention != "" {
		t.Fatalf("prompt cache retention = %q, want empty for custom base URL", retention)
	}
}

func TestDefaultWorkerPromptCacheSettingsDoesNotForceUnsupportedRetention(t *testing.T) {
	t.Parallel()

	key, retention := defaultWorkerPromptCacheSettings(cpstore.RunnableAgent{
		AgentRecord: cpstore.AgentRecord{
			NamespaceID: "default",
			ID:          "worker-a",
			ModelName:   "gpt-4",
		},
	})

	if key != "server:default:worker-a" {
		t.Fatalf("prompt cache key = %q, want %q", key, "server:default:worker-a")
	}
	if retention != "" {
		t.Fatalf("prompt cache retention = %q, want empty for unsupported model", retention)
	}
}

func TestMultiProviderWorkerDriverFactoryDefaultsEmptyProviderToOpenAI(t *testing.T) {
	t.Parallel()

	driver, err := MultiProviderWorkerDriverFactory{
		OpenAIAPIKey: "openai-test-key",
	}.NewWorkerDriver(context.Background(), cpstore.RunnableAgent{
		AgentRecord: cpstore.AgentRecord{
			NamespaceID: "default",
			ID:          "worker-a",
			ModelName:   "gpt-5",
		},
	})
	if err != nil {
		t.Fatalf("NewWorkerDriver() error = %v", err)
	}
	if _, ok := driver.(*agentopenai.Driver); !ok {
		t.Fatalf("driver = %#v, want *agentopenai.Driver", driver)
	}
}

func TestMultiProviderWorkerDriverFactoryBuildsAnthropicDriver(t *testing.T) {
	t.Parallel()

	driver, err := MultiProviderWorkerDriverFactory{
		AnthropicAPIKey: "anthropic-test-key",
	}.NewWorkerDriver(context.Background(), cpstore.RunnableAgent{
		AgentRecord: cpstore.AgentRecord{
			NamespaceID:   "default",
			ID:            "worker-a",
			ModelProvider: "anthropic",
			ModelName:     "claude-sonnet-4-6",
		},
	})
	if err != nil {
		t.Fatalf("NewWorkerDriver() error = %v", err)
	}
	if _, ok := driver.(*agentanthropic.Driver); !ok {
		t.Fatalf("driver = %#v, want *agentanthropic.Driver", driver)
	}
}

func TestMultiProviderWorkerDriverFactoryRejectsUnsupportedProvider(t *testing.T) {
	t.Parallel()

	_, err := MultiProviderWorkerDriverFactory{}.NewWorkerDriver(context.Background(), cpstore.RunnableAgent{
		AgentRecord: cpstore.AgentRecord{
			NamespaceID:   "default",
			ID:            "worker-a",
			ModelProvider: "banana",
			ModelName:     "banana-1",
		},
	})
	if err == nil {
		t.Fatal("NewWorkerDriver() error = nil, want unsupported provider error")
	}
}
