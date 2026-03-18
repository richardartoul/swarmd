package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/richardartoul/swarmd/pkg/agent"
	agentanthropic "github.com/richardartoul/swarmd/pkg/agent/anthropic"
	agentopenai "github.com/richardartoul/swarmd/pkg/agent/openai"
	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

type OpenAIWorkerDriverFactory struct {
	APIKey     string
	HTTPClient *http.Client
}

func (f OpenAIWorkerDriverFactory) NewWorkerDriver(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
	if record.ModelProvider != "" && record.ModelProvider != "openai" {
		return nil, fmt.Errorf("unsupported worker model provider %q for agent %q/%q", record.ModelProvider, record.NamespaceID, record.ID)
	}
	promptCacheKey, promptCacheRetention := defaultWorkerPromptCacheSettings(record)
	driver, err := agentopenai.New(agentopenai.Config{
		APIKey:               f.APIKey,
		BaseURL:              record.ModelBaseURL,
		Model:                record.ModelName,
		HTTPClient:           f.HTTPClient,
		PromptCacheKey:       promptCacheKey,
		PromptCacheRetention: promptCacheRetention,
	})
	if err != nil {
		return nil, err
	}
	return driver, nil
}

type AnthropicWorkerDriverFactory struct {
	APIKey     string
	HTTPClient *http.Client
}

func (f AnthropicWorkerDriverFactory) NewWorkerDriver(_ context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
	if strings.TrimSpace(record.ModelProvider) != "anthropic" {
		return nil, fmt.Errorf("unsupported worker model provider %q for agent %q/%q", record.ModelProvider, record.NamespaceID, record.ID)
	}
	promptCacheTTL := defaultAnthropicWorkerPromptCacheTTL(record)
	driver, err := agentanthropic.New(agentanthropic.Config{
		APIKey:         f.APIKey,
		BaseURL:        record.ModelBaseURL,
		Model:          record.ModelName,
		HTTPClient:     f.HTTPClient,
		PromptCacheTTL: promptCacheTTL,
	})
	if err != nil {
		return nil, err
	}
	return driver, nil
}

type MultiProviderWorkerDriverFactory struct {
	OpenAIAPIKey    string
	AnthropicAPIKey string
	HTTPClient      *http.Client
}

func (f MultiProviderWorkerDriverFactory) NewWorkerDriver(ctx context.Context, record cpstore.RunnableAgent) (agent.Driver, error) {
	switch strings.TrimSpace(record.ModelProvider) {
	case "", "openai":
		return OpenAIWorkerDriverFactory{
			APIKey:     f.OpenAIAPIKey,
			HTTPClient: f.HTTPClient,
		}.NewWorkerDriver(ctx, record)
	case "anthropic":
		return AnthropicWorkerDriverFactory{
			APIKey:     f.AnthropicAPIKey,
			HTTPClient: f.HTTPClient,
		}.NewWorkerDriver(ctx, record)
	default:
		return nil, fmt.Errorf("unsupported worker model provider %q for agent %q/%q", record.ModelProvider, record.NamespaceID, record.ID)
	}
}

func defaultWorkerPromptCacheSettings(record cpstore.RunnableAgent) (string, string) {
	if normalizeOpenAIBaseURL(record.ModelBaseURL) != agentopenai.DefaultBaseURL {
		return "", ""
	}
	key := fmt.Sprintf("server:%s:%s", record.NamespaceID, record.ID)
	retention := ""
	if supportsExtendedPromptCache(baseModel(record.ModelName)) {
		retention = "24h"
	}
	return key, retention
}

func normalizeOpenAIBaseURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return agentopenai.DefaultBaseURL
	}
	return baseURL
}

func baseModel(model string) string {
	model = strings.TrimSpace(model)
	for _, effort := range []string{"xhigh", "high", "medium", "minimal", "low", "none"} {
		suffix := "-x" + effort
		if base, ok := strings.CutSuffix(model, suffix); ok && strings.TrimSpace(base) != "" {
			return strings.TrimSpace(base)
		}
	}
	return model
}

func supportsExtendedPromptCache(model string) bool {
	switch {
	case model == "gpt-4.1":
		return true
	case strings.HasPrefix(model, "gpt-4.1-"):
		return true
	case strings.HasPrefix(model, "gpt-5"):
		return true
	default:
		return false
	}
}

func defaultAnthropicWorkerPromptCacheTTL(record cpstore.RunnableAgent) string {
	return "1h"
}
