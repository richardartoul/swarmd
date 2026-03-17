package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunInitScaffoldsHeartbeatConfig(t *testing.T) {
	t.Parallel()

	configRoot := filepath.Join(t.TempDir(), "server-config")
	agentPath := filepath.Join(configRoot, "namespaces", initDefaultNamespace, "agents", initHeartbeatAgentFile)

	var stdout bytes.Buffer
	if err := run(context.Background(), []string{"init", "-config-root", configRoot}, commandIO{
		stdout: &stdout,
		stderr: &stdout,
	}); err != nil {
		t.Fatalf("run(init) error = %v", err)
	}

	contents, err := os.ReadFile(agentPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", agentPath, err)
	}
	if string(contents) != initHeartbeatAgentSpec {
		t.Fatalf("agent config contents = %q, want scaffold template", string(contents))
	}

	output := stdout.String()
	if !strings.Contains(output, "init> config_root="+configRoot) {
		t.Fatalf("init output = %q, want config root summary", output)
	}
	if !strings.Contains(output, "created> "+agentPath) {
		t.Fatalf("init output = %q, want created path", output)
	}
}

func TestRunInitRejectsExistingAgentConfig(t *testing.T) {
	t.Parallel()

	configRoot := filepath.Join(t.TempDir(), "server-config")
	if err := run(context.Background(), []string{"init", "-config-root", configRoot}, commandIO{
		stdout: &bytes.Buffer{},
		stderr: &bytes.Buffer{},
	}); err != nil {
		t.Fatalf("first run(init) error = %v", err)
	}

	err := run(context.Background(), []string{"init", "-config-root", configRoot}, commandIO{
		stdout: &bytes.Buffer{},
		stderr: &bytes.Buffer{},
	})
	if err == nil {
		t.Fatal("second run(init) error = nil, want existing config failure")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second run(init) error = %v, want existing config error", err)
	}
}

func TestRunInitGeneratedConfigValidates(t *testing.T) {
	t.Parallel()

	configRoot := filepath.Join(t.TempDir(), "server-config")
	if err := run(context.Background(), []string{"init", "-config-root", configRoot}, commandIO{
		stdout: &bytes.Buffer{},
		stderr: &bytes.Buffer{},
	}); err != nil {
		t.Fatalf("run(init) error = %v", err)
	}

	var stdout bytes.Buffer
	if err := run(context.Background(), []string{"config", "validate", "-config-root", configRoot}, commandIO{
		stdout: &stdout,
		stderr: &stdout,
	}); err != nil {
		t.Fatalf("run(config validate) error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "config> valid") {
		t.Fatalf("config validate output = %q, want valid summary", output)
	}
	if !strings.Contains(output, "namespaces=1 agents=1 schedules=1") {
		t.Fatalf("config validate output = %q, want namespace/agent/schedule counts", output)
	}
}
