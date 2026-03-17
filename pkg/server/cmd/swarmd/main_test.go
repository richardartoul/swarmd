package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/richardartoul/swarmd/pkg/server"
)

func TestRunRejectsUnknownSubcommand(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	err := run(context.Background(), []string{"wat"}, commandIO{
		stdout: &stderr,
		stderr: &stderr,
	})
	if err == nil {
		t.Fatal("run() error = nil, want unknown subcommand error")
	}
	if !strings.Contains(err.Error(), "unknown subcommand") {
		t.Fatalf("run() error = %v, want unknown subcommand", err)
	}
}

func TestRunHelpIncludesInitSubcommand(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	if err := run(context.Background(), []string{"help"}, commandIO{
		stdout: &stdout,
		stderr: &stdout,
	}); err != nil {
		t.Fatalf("run(help) error = %v", err)
	}

	if output := stdout.String(); !strings.Contains(output, "init              Scaffold a config root with a default heartbeat agent.") {
		t.Fatalf("help output = %q, want init subcommand", output)
	}
}

func TestParseServerConfigDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := parseServerConfig(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseServerConfig() error = %v", err)
	}
	if cfg.dbPath == "" {
		t.Fatal("dbPath was empty")
	}
	if cfg.configRoot == "" {
		t.Fatal("configRoot was empty")
	}
	if cfg.dataDir != defaultDataDir {
		t.Fatalf("dataDir = %q, want %q", cfg.dataDir, defaultDataDir)
	}
	if cfg.dbPath != defaultSQLitePath {
		t.Fatalf("dbPath = %q, want %q", cfg.dbPath, defaultSQLitePath)
	}
	if cfg.rootBase != defaultRootBase {
		t.Fatalf("rootBase = %q, want %q", cfg.rootBase, defaultRootBase)
	}
}

func TestParseServerConfigDataDirOverridesDerivedPaths(t *testing.T) {
	t.Parallel()

	dataDir := filepath.Join(t.TempDir(), "server-data")
	cfg, err := parseServerConfig([]string{"-data-dir", dataDir}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseServerConfig() error = %v", err)
	}
	if cfg.dbPath != filepath.Join(dataDir, defaultSQLiteFilename) {
		t.Fatalf("dbPath = %q, want %q", cfg.dbPath, filepath.Join(dataDir, defaultSQLiteFilename))
	}
	if cfg.rootBase == "" {
		t.Fatal("rootBase was empty")
	}
	if cfg.rootBase != filepath.Join(dataDir, defaultAgentRootsDir) {
		t.Fatalf("rootBase = %q, want %q", cfg.rootBase, filepath.Join(dataDir, defaultAgentRootsDir))
	}
}

func TestParseServerConfigExplicitPathsOverrideDataDir(t *testing.T) {
	t.Parallel()

	dataDir := filepath.Join(t.TempDir(), "server-data")
	dbPath := filepath.Join(t.TempDir(), "custom.db")
	rootBase := filepath.Join(t.TempDir(), "custom-roots")
	cfg, err := parseServerConfig([]string{
		"-data-dir", dataDir,
		"-db", dbPath,
		"-root-base", rootBase,
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseServerConfig() error = %v", err)
	}
	if cfg.dbPath != dbPath {
		t.Fatalf("dbPath = %q, want %q", cfg.dbPath, dbPath)
	}
	if cfg.rootBase != rootBase {
		t.Fatalf("rootBase = %q, want %q", cfg.rootBase, rootBase)
	}
}

func TestParseServerConfigReadsAnthropicAPIKeyFromEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-test-key")

	cfg, err := parseServerConfig(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseServerConfig() error = %v", err)
	}
	if cfg.anthropicAPIKey != "anthropic-test-key" {
		t.Fatalf("anthropicAPIKey = %q, want %q", cfg.anthropicAPIKey, "anthropic-test-key")
	}
}

func TestValidateServerAPIKeysRequiresOpenAIForDefaultProvider(t *testing.T) {
	t.Parallel()

	err := validateServerAPIKeys([]server.AgentSpec{{
		Model:  server.AgentModelSpec{Name: "gpt-5"},
		Prompt: "hello",
	}}, serverConfig{})
	if err == nil {
		t.Fatal("validateServerAPIKeys() error = nil, want openai key requirement")
	}
	if !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Fatalf("validateServerAPIKeys() error = %v, want openai key requirement", err)
	}
}

func TestValidateServerAPIKeysRequiresAnthropicKeyForAnthropicProvider(t *testing.T) {
	t.Parallel()

	err := validateServerAPIKeys([]server.AgentSpec{{
		Model:  server.AgentModelSpec{Provider: "anthropic", Name: "claude-sonnet-4-6"},
		Prompt: "hello",
	}}, serverConfig{})
	if err == nil {
		t.Fatal("validateServerAPIKeys() error = nil, want anthropic key requirement")
	}
	if !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Fatalf("validateServerAPIKeys() error = %v, want anthropic key requirement", err)
	}
}

func TestValidateServerAPIKeysAcceptsConfiguredProviderKeys(t *testing.T) {
	t.Parallel()

	err := validateServerAPIKeys([]server.AgentSpec{
		{
			Model:  server.AgentModelSpec{Name: "gpt-5"},
			Prompt: "hello",
		},
		{
			Model:  server.AgentModelSpec{Provider: "anthropic", Name: "claude-sonnet-4-6"},
			Prompt: "hello",
		},
	}, serverConfig{
		openAIAPIKey:    "openai-test-key",
		anthropicAPIKey: "anthropic-test-key",
	})
	if err != nil {
		t.Fatalf("validateServerAPIKeys() error = %v, want nil", err)
	}
}
