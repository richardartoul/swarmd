package agent_test

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/richardartoul/swarmd/pkg/agent"
	"github.com/richardartoul/swarmd/pkg/sh/interp"
)

func TestManualDuckDuckGoWebSearch(t *testing.T) {
	if os.Getenv("SWARMD_RUN_MANUAL_WEB_SEARCH") == "" {
		t.Skip("set SWARMD_RUN_MANUAL_WEB_SEARCH=1 to run; optionally set SWARMD_WEB_SEARCH_QUERIES and SWARMD_WEB_SEARCH_LIMIT")
	}

	queries := manualWebSearchQueries()
	if len(queries) == 0 {
		t.Fatal("manual web search query list is empty")
	}

	limit := 5
	if raw := strings.TrimSpace(os.Getenv("SWARMD_WEB_SEARCH_LIMIT")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("strconv.Atoi(SWARMD_WEB_SEARCH_LIMIT) error = %v", err)
		}
		if parsed > 0 {
			limit = parsed
		}
	}
	if limit > 10 {
		limit = 10
	}

	clientFactory, err := interp.NewHTTPClientFactory(interp.OSNetworkDialer{}, nil)
	if err != nil {
		t.Fatalf("interp.NewHTTPClientFactory() error = %v", err)
	}
	backend := agent.NewDuckDuckGoWebSearchBackend()

	for _, query := range queries {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		response, err := backend.Search(ctx, clientFactory, query, limit)
		cancel()

		if err != nil {
			t.Errorf("query=%q error = %v", query, err)
			continue
		}

		t.Logf("query=%q provider=%s results=%d", query, response.Provider, len(response.Results))
		for idx, result := range response.Results {
			t.Logf("  %d|%s|%s", idx+1, result.Title, result.URL)
			if result.Snippet != "" {
				t.Logf("    %s", result.Snippet)
			}
		}
	}
}

func manualWebSearchQueries() []string {
	raw := strings.TrimSpace(os.Getenv("SWARMD_WEB_SEARCH_QUERIES"))
	if raw == "" {
		return []string{
			"go net/http package",
			"golang html parser",
			"duckduckgo html search results",
		}
	}

	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == ';'
	})
	queries := make([]string, 0, len(parts))
	for _, part := range parts {
		if query := strings.TrimSpace(part); query != "" {
			queries = append(queries, query)
		}
	}
	return queries
}
