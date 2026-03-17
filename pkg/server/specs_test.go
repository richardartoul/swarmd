package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAgentSpecs(t *testing.T) {
	t.Parallel()

	configRoot := t.TempDir()
	writeAgentSpec(t, configRoot, "default", "google-curl-check", `
version: 1
name: Google Curl Check
description: Checks google over HTTP.
model:
  name: gpt-5
prompt: |
  Run curl https://www.google.com and report whether it worked.
network:
  reachable_hosts:
    - glob: "*"
tools:
  - server_log
runtime:
  max_steps: 4
  step_timeout: 45s
schedules:
  - cron: "* * * * *"
`)

	specs, err := LoadAgentSpecs(configRoot)
	if err != nil {
		t.Fatalf("LoadAgentSpecs() error = %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("len(specs) = %d, want 1", len(specs))
	}
	spec := specs[0]
	if spec.NamespaceID != "default" {
		t.Fatalf("spec.NamespaceID = %q, want %q", spec.NamespaceID, "default")
	}
	if spec.AgentID != "google-curl-check" {
		t.Fatalf("spec.AgentID = %q, want %q", spec.AgentID, "google-curl-check")
	}
	if spec.Version == nil || *spec.Version != 1 {
		t.Fatalf("spec.Version = %#v, want 1", spec.Version)
	}
	if spec.Model.Name != "gpt-5" {
		t.Fatalf("spec.Model.Name = %q, want %q", spec.Model.Name, "gpt-5")
	}
	if spec.Network == nil || len(spec.Network.ReachableHosts) != 1 || spec.Network.ReachableHosts[0].Glob != "*" {
		t.Fatalf("spec.Network = %#v, want reachable_hosts glob *", spec.Network)
	}
	if len(spec.Tools) != 1 || spec.Tools[0].ID != "server_log" {
		t.Fatalf("spec.Tools = %#v, want [server_log]", spec.Tools)
	}
	if len(spec.Schedules) != 1 || spec.Schedules[0].CronExpr != "* * * * *" {
		t.Fatalf("spec.Schedules = %#v, want one every-minute schedule", spec.Schedules)
	}
}

func TestLoadAgentSpecsRejectsMissingVersion(t *testing.T) {
	t.Parallel()

	configRoot := t.TempDir()
	writeAgentSpec(t, configRoot, "default", "worker", `
model:
  name: gpt-5
prompt: hello
`)

	_, err := LoadAgentSpecs(configRoot)
	if err == nil {
		t.Fatal("LoadAgentSpecs() error = nil, want missing version error")
	}
	if !strings.Contains(err.Error(), "version must be set") {
		t.Fatalf("LoadAgentSpecs() error = %v, want missing version error", err)
	}
}

func TestLoadAgentSpecsRejectsUnsupportedVersion(t *testing.T) {
	t.Parallel()

	configRoot := t.TempDir()
	writeAgentSpec(t, configRoot, "default", "worker", `
version: 2
model:
  name: gpt-5
prompt: hello
`)

	_, err := LoadAgentSpecs(configRoot)
	if err == nil {
		t.Fatal("LoadAgentSpecs() error = nil, want unsupported version error")
	}
	if !strings.Contains(err.Error(), "unsupported version 2") {
		t.Fatalf("LoadAgentSpecs() error = %v, want unsupported version error", err)
	}
}

func TestLoadAgentSpecsAcceptsSupportedModelProviders(t *testing.T) {
	t.Parallel()

	configRoot := t.TempDir()
	writeAgentSpec(t, configRoot, "default", "openai-worker", `
version: 1
model:
  provider: openai
  name: gpt-5
prompt: hello
`)
	writeAgentSpec(t, configRoot, "default", "anthropic-worker", `
version: 1
model:
  provider: anthropic
  name: claude-sonnet-4-6
prompt: hello
`)

	specs, err := LoadAgentSpecs(configRoot)
	if err != nil {
		t.Fatalf("LoadAgentSpecs() error = %v", err)
	}
	if len(specs) != 2 {
		t.Fatalf("len(specs) = %d, want 2", len(specs))
	}
	if specs[0].Model.Provider != "anthropic" && specs[1].Model.Provider != "anthropic" {
		t.Fatalf("specs = %#v, want anthropic provider preserved", specs)
	}
	if specs[0].Model.Provider != "openai" && specs[1].Model.Provider != "openai" {
		t.Fatalf("specs = %#v, want openai provider preserved", specs)
	}
}

func TestLoadAgentSpecsRejectsUnsupportedModelProvider(t *testing.T) {
	t.Parallel()

	configRoot := t.TempDir()
	writeAgentSpec(t, configRoot, "default", "worker", `
version: 1
model:
  provider: banana
  name: gpt-5
prompt: hello
`)

	_, err := LoadAgentSpecs(configRoot)
	if err == nil {
		t.Fatal("LoadAgentSpecs() error = nil, want invalid provider error")
	}
	if !strings.Contains(err.Error(), `model.provider must be empty, "openai", or "anthropic"`) {
		t.Fatalf("LoadAgentSpecs() error = %v, want provider validation error", err)
	}
}

func TestLoadAgentSpecsIncludesMemorySettings(t *testing.T) {
	t.Parallel()

	configRoot := t.TempDir()
	writeAgentSpec(t, configRoot, "default", "worker", `
version: 1
model:
  name: gpt-5
prompt: hello
memory:
  prompt_override: |
    Read .memory/ROOT.md before opening deeper memory files.
`)

	specs, err := LoadAgentSpecs(configRoot)
	if err != nil {
		t.Fatalf("LoadAgentSpecs() error = %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("len(specs) = %d, want 1", len(specs))
	}
	if got := strings.TrimSpace(specs[0].Memory.PromptOverride); got != "Read .memory/ROOT.md before opening deeper memory files." {
		t.Fatalf("specs[0].Memory.PromptOverride = %q, want memory prompt override", got)
	}
	if specs[0].Memory.Disable {
		t.Fatal("specs[0].Memory.Disable = true, want false")
	}
}

func TestLoadAgentSpecsIncludesMountSettings(t *testing.T) {
	t.Parallel()

	configRoot := t.TempDir()
	sourcePath := filepath.Join(configRoot, "namespaces", "default", "seed.txt")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", filepath.Dir(sourcePath), err)
	}
	if err := os.WriteFile(sourcePath, []byte("seed mount"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", sourcePath, err)
	}
	writeAgentSpec(t, configRoot, "default", "worker", `
version: 1
model:
  name: gpt-5
prompt: hello
mounts:
  - path: mounted/seed.txt
    description: Seed file mounted from config.
    source:
      path: ../seed.txt
  - path: mounted/inline.txt
    source:
      inline: ""
  - path: mounted/secret.txt
    source:
      env_var: EXAMPLE_API_KEY
`)

	specs, err := LoadAgentSpecs(configRoot)
	if err != nil {
		t.Fatalf("LoadAgentSpecs() error = %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("len(specs) = %d, want 1", len(specs))
	}
	if got := len(specs[0].Mounts); got != 3 {
		t.Fatalf("len(specs[0].Mounts) = %d, want 3", got)
	}
	if got := specs[0].Mounts[0].Path; got != "mounted/seed.txt" {
		t.Fatalf("specs[0].Mounts[0].Path = %q, want %q", got, "mounted/seed.txt")
	}
	if got := specs[0].Mounts[0].Description; got != "Seed file mounted from config." {
		t.Fatalf("specs[0].Mounts[0].Description = %q, want mount description", got)
	}
	if got := specs[0].Mounts[0].Source.Path; got != "../seed.txt" {
		t.Fatalf("specs[0].Mounts[0].Source.Path = %q, want %q", got, "../seed.txt")
	}
	if specs[0].Mounts[0].Source.Inline != nil {
		t.Fatal("specs[0].Mounts[0].Source.Inline != nil, want nil for path mount")
	}
	if specs[0].Mounts[1].Source.Inline == nil {
		t.Fatal("specs[0].Mounts[1].Source.Inline = nil, want inline content pointer")
	}
	if got := *specs[0].Mounts[1].Source.Inline; got != "" {
		t.Fatalf("*specs[0].Mounts[1].Source.Inline = %q, want empty string", got)
	}
	if got := specs[0].Mounts[2].Source.EnvVar; got != "EXAMPLE_API_KEY" {
		t.Fatalf("specs[0].Mounts[2].Source.EnvVar = %q, want %q", got, "EXAMPLE_API_KEY")
	}
}

func TestLoadAgentSpecsIncludesHTTPHeaderSettings(t *testing.T) {
	t.Parallel()

	configRoot := t.TempDir()
	writeAgentSpec(t, configRoot, "default", "worker", `
version: 1
model:
  name: gpt-5
prompt: hello
http:
  headers:
    - name: Authorization
      env_var: EXAMPLE_AUTHORIZATION
      domains:
        - glob: "*.example.com"
        - regex: ^payments-[a-z0-9-]+\.example\.com$
    - name: User-Agent
      value: swarmd-test
`)

	specs, err := LoadAgentSpecs(configRoot)
	if err != nil {
		t.Fatalf("LoadAgentSpecs() error = %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("len(specs) = %d, want 1", len(specs))
	}
	if got := len(specs[0].HTTP.Headers); got != 2 {
		t.Fatalf("len(specs[0].HTTP.Headers) = %d, want 2", got)
	}
	if got := specs[0].HTTP.Headers[0].Name; got != "Authorization" {
		t.Fatalf("specs[0].HTTP.Headers[0].Name = %q, want %q", got, "Authorization")
	}
	if got := specs[0].HTTP.Headers[0].EnvVar; got != "EXAMPLE_AUTHORIZATION" {
		t.Fatalf("specs[0].HTTP.Headers[0].EnvVar = %q, want %q", got, "EXAMPLE_AUTHORIZATION")
	}
	if got := len(specs[0].HTTP.Headers[0].Domains); got != 2 {
		t.Fatalf("len(specs[0].HTTP.Headers[0].Domains) = %d, want 2", got)
	}
	if got := specs[0].HTTP.Headers[0].Domains[0].Glob; got != "*.example.com" {
		t.Fatalf("specs[0].HTTP.Headers[0].Domains[0].Glob = %q, want %q", got, "*.example.com")
	}
	if got := specs[0].HTTP.Headers[0].Domains[1].Regex; got != "^payments-[a-z0-9-]+\\.example\\.com$" {
		t.Fatalf("specs[0].HTTP.Headers[0].Domains[1].Regex = %q, want regex", got)
	}
	if specs[0].HTTP.Headers[1].Value == nil {
		t.Fatal("specs[0].HTTP.Headers[1].Value = nil, want literal value")
	}
	if got := *specs[0].HTTP.Headers[1].Value; got != "swarmd-test" {
		t.Fatalf("*specs[0].HTTP.Headers[1].Value = %q, want %q", got, "swarmd-test")
	}
}

func TestLoadAgentSpecsRejectsMemoryPromptOverrideWhenDisabled(t *testing.T) {
	t.Parallel()

	configRoot := t.TempDir()
	writeAgentSpec(t, configRoot, "default", "worker", `
version: 1
model:
  name: gpt-5
prompt: hello
memory:
  disable: true
  prompt_override: do not use
`)

	_, err := LoadAgentSpecs(configRoot)
	if err == nil {
		t.Fatal("LoadAgentSpecs() error = nil, want invalid memory config error")
	}
	if !strings.Contains(err.Error(), "memory.prompt_override") {
		t.Fatalf("LoadAgentSpecs() error = %v, want memory.prompt_override validation error", err)
	}
}

func TestLoadAgentSpecsRejectsInvalidFilesystemKind(t *testing.T) {
	t.Parallel()

	configRoot := t.TempDir()
	writeAgentSpec(t, configRoot, "default", "worker", `
version: 1
model:
  name: gpt-5
prompt: hello
runtime:
  filesystem:
    kind: network
`)

	_, err := LoadAgentSpecs(configRoot)
	if err == nil {
		t.Fatal("LoadAgentSpecs() error = nil, want invalid filesystem kind error")
	}
	if !strings.Contains(err.Error(), "runtime.filesystem.kind") {
		t.Fatalf("LoadAgentSpecs() error = %v, want runtime.filesystem.kind validation error", err)
	}
}

func TestLoadAgentSpecsRejectsInvalidMountSettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		setup    func(t *testing.T, configRoot string)
		contents string
		wantErr  string
	}{
		{
			name: "missing source and content",
			contents: `
version: 1
model:
  name: gpt-5
prompt: hello
mounts:
  - path: mounted/file.txt
`,
			wantErr: "exactly one of source.path, source.env_var, or source.inline",
		},
		{
			name: "both source and content",
			setup: func(t *testing.T, configRoot string) {
				path := filepath.Join(configRoot, "namespaces", "default", "seed.txt")
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("os.MkdirAll(%q) error = %v", filepath.Dir(path), err)
				}
				if err := os.WriteFile(path, []byte("seed"), 0o644); err != nil {
					t.Fatalf("os.WriteFile(%q) error = %v", path, err)
				}
			},
			contents: `
version: 1
model:
  name: gpt-5
prompt: hello
mounts:
  - path: mounted/file.txt
    source:
      path: ../seed.txt
      inline: inline
`,
			wantErr: "exactly one of source.path, source.env_var, or source.inline",
		},
		{
			name: "duplicate target path",
			contents: `
version: 1
model:
  name: gpt-5
prompt: hello
mounts:
  - path: mounted/file.txt
    source:
      inline: one
  - path: /mounted/file.txt
    source:
      inline: two
`,
			wantErr: "duplicates mounts[0]",
		},
		{
			name: "missing source path",
			contents: `
version: 1
model:
  name: gpt-5
prompt: hello
mounts:
  - path: mounted/file.txt
    source:
      path: ../missing-source.txt
`,
			wantErr: "stat",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configRoot := t.TempDir()
			if tt.setup != nil {
				tt.setup(t, configRoot)
			}
			writeAgentSpec(t, configRoot, "default", "worker", tt.contents)

			_, err := LoadAgentSpecs(configRoot)
			if err == nil {
				t.Fatal("LoadAgentSpecs() error = nil, want invalid mount config error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("LoadAgentSpecs() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadAgentSpecsRejectsInvalidHTTPHeaderSettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		contents string
		wantErr  string
	}{
		{
			name: "missing value and env var",
			contents: `
version: 1
model:
  name: gpt-5
prompt: hello
http:
  headers:
    - name: Authorization
`,
			wantErr: "exactly one of value or env_var",
		},
		{
			name: "both value and env var",
			contents: `
version: 1
model:
  name: gpt-5
prompt: hello
http:
  headers:
    - name: Authorization
      value: literal
      env_var: EXAMPLE_AUTHORIZATION
`,
			wantErr: "exactly one of value or env_var",
		},
		{
			name: "invalid domain matcher",
			contents: `
version: 1
model:
  name: gpt-5
prompt: hello
http:
  headers:
    - name: Authorization
      value: literal
      domains:
        - glob: "["
`,
			wantErr: "domains[0].glob invalid",
		},
		{
			name: "invalid regex matcher",
			contents: `
version: 1
model:
  name: gpt-5
prompt: hello
http:
  headers:
    - name: Authorization
      value: literal
      domains:
        - regex: "("
`,
			wantErr: "domains[0].regex invalid",
		},
		{
			name: "glob incorrectly includes protocol",
			contents: `
version: 1
model:
  name: gpt-5
prompt: hello
http:
  headers:
    - name: Authorization
      value: literal
      domains:
        - glob: "https://api.example.com"
`,
			wantErr: "protocol prefixes like http:// or https:// are not allowed",
		},
		{
			name: "regex incorrectly includes protocol",
			contents: `
version: 1
model:
  name: gpt-5
prompt: hello
http:
  headers:
    - name: Authorization
      value: literal
      domains:
        - regex: "^https?:\\/\\/api\\.example\\.com$"
`,
			wantErr: "protocol prefixes like http:// or https:// are not allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configRoot := t.TempDir()
			writeAgentSpec(t, configRoot, "default", "worker", tt.contents)

			_, err := LoadAgentSpecs(configRoot)
			if err == nil {
				t.Fatal("LoadAgentSpecs() error = nil, want invalid HTTP config error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("LoadAgentSpecs() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateReferencedAgentConfigEnvRejectsMissingVars(t *testing.T) {
	t.Parallel()

	err := ValidateReferencedAgentConfigEnv([]AgentSpec{{
		NamespaceID: "default",
		AgentID:     "worker",
		Mounts: []AgentMountSpec{{
			Path: "mounted/secret.txt",
			Source: AgentMountSourceSpec{
				EnvVar: "EXAMPLE_API_KEY",
			},
		}},
		HTTP: AgentHTTPSpec{
			Headers: []AgentHTTPHeaderSpec{{
				Name:   "Authorization",
				EnvVar: "EXAMPLE_AUTHORIZATION",
			}},
		},
	}}, func(string) string {
		return ""
	})
	if err == nil {
		t.Fatal("ValidateReferencedAgentConfigEnv() error = nil, want missing env error")
	}
	if !strings.Contains(err.Error(), "EXAMPLE_API_KEY") || !strings.Contains(err.Error(), "EXAMPLE_AUTHORIZATION") {
		t.Fatalf("ValidateReferencedAgentConfigEnv() error = %v, want both missing env vars", err)
	}
}

func TestValidateReferencedAgentConfigEnvIgnoresSatisfiedVars(t *testing.T) {
	t.Parallel()

	err := ValidateReferencedAgentConfigEnv([]AgentSpec{{
		NamespaceID: "default",
		AgentID:     "worker",
		Mounts: []AgentMountSpec{{
			Path: "mounted/secret.txt",
			Source: AgentMountSourceSpec{
				EnvVar: "EXAMPLE_API_KEY",
			},
		}},
	}}, func(key string) string {
		if key == "EXAMPLE_API_KEY" {
			return "secret"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("ValidateReferencedAgentConfigEnv() error = %v, want nil", err)
	}
}

func TestLoadAgentSpecsRejectsUnknownTool(t *testing.T) {
	t.Parallel()

	configRoot := t.TempDir()
	writeAgentSpec(t, configRoot, "default", "worker", `
version: 1
model:
  name: gpt-5
prompt: hello
tools:
  - unknown_command
`)

	_, err := LoadAgentSpecs(configRoot)
	if err == nil {
		t.Fatal("LoadAgentSpecs() error = nil, want tool validation error")
	}
	if !strings.Contains(err.Error(), "tools") {
		t.Fatalf("LoadAgentSpecs() error = %v, want tool validation error", err)
	}
}

func TestLoadAgentSpecsAcceptsDatadogReadTool(t *testing.T) {
	t.Parallel()

	configRoot := t.TempDir()
	writeAgentSpec(t, configRoot, "default", "datadog-reader", `
version: 1
model:
  name: gpt-5
prompt: hello
network:
  reachable_hosts:
    - glob: "*"
tools:
  - datadog_read
`)

	specs, err := LoadAgentSpecs(configRoot)
	if err != nil {
		t.Fatalf("LoadAgentSpecs() error = %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("len(specs) = %d, want 1", len(specs))
	}
	if len(specs[0].Tools) != 1 || specs[0].Tools[0].ID != "datadog_read" {
		t.Fatalf("specs[0].Tools = %#v, want [%s]", specs[0].Tools, "datadog_read")
	}
}

func TestLoadAgentSpecsIncludesNetworkSettings(t *testing.T) {
	t.Parallel()

	configRoot := t.TempDir()
	writeAgentSpec(t, configRoot, "default", "worker", `
version: 1
model:
  name: gpt-5
prompt: hello
network:
  reachable_hosts:
    - glob: "*.example.com"
    - regex: api-[a-z0-9-]+\.corp\.internal
`)

	specs, err := LoadAgentSpecs(configRoot)
	if err != nil {
		t.Fatalf("LoadAgentSpecs() error = %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("len(specs) = %d, want 1", len(specs))
	}
	if specs[0].Network == nil {
		t.Fatal("specs[0].Network = nil, want network settings")
	}
	if got := len(specs[0].Network.ReachableHosts); got != 2 {
		t.Fatalf("len(specs[0].Network.ReachableHosts) = %d, want 2", got)
	}
	if got := specs[0].Network.ReachableHosts[0].Glob; got != "*.example.com" {
		t.Fatalf("specs[0].Network.ReachableHosts[0].Glob = %q, want %q", got, "*.example.com")
	}
	if got := specs[0].Network.ReachableHosts[1].Regex; got != "api-[a-z0-9-]+\\.corp\\.internal" {
		t.Fatalf("specs[0].Network.ReachableHosts[1].Regex = %q, want unanchored regex", got)
	}
}

func TestLoadAgentSpecsRejectsLegacyAllowNetworkCapability(t *testing.T) {
	t.Parallel()

	configRoot := t.TempDir()
	writeAgentSpec(t, configRoot, "default", "worker", `
version: 1
model:
  name: gpt-5
prompt: hello
capabilities:
  allow_network: true
`)

	_, err := LoadAgentSpecs(configRoot)
	if err == nil {
		t.Fatal("LoadAgentSpecs() error = nil, want legacy allow_network rejection")
	}
	if !strings.Contains(err.Error(), "capabilities.allow_network is no longer supported") {
		t.Fatalf("LoadAgentSpecs() error = %v, want legacy allow_network rejection", err)
	}
}

func TestLoadAgentSpecsRejectsInvalidNetworkSettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		contents string
		wantErr  string
	}{
		{
			name: "empty reachable hosts",
			contents: `
version: 1
model:
  name: gpt-5
prompt: hello
network:
  reachable_hosts: []
`,
			wantErr: "network.reachable_hosts must not be empty",
		},
		{
			name: "invalid network glob",
			contents: `
version: 1
model:
  name: gpt-5
prompt: hello
network:
  reachable_hosts:
    - glob: "["
`,
			wantErr: "network.reachable_hosts[0].glob invalid",
		},
		{
			name: "invalid network regex",
			contents: `
version: 1
model:
  name: gpt-5
prompt: hello
network:
  reachable_hosts:
    - regex: "("
`,
			wantErr: "network.reachable_hosts[0].regex invalid",
		},
		{
			name: "network glob incorrectly includes protocol",
			contents: `
version: 1
model:
  name: gpt-5
prompt: hello
network:
  reachable_hosts:
    - glob: "https://api.example.com"
`,
			wantErr: "protocol prefixes like http:// or https:// are not allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configRoot := t.TempDir()
			writeAgentSpec(t, configRoot, "default", "worker", tt.contents)

			_, err := LoadAgentSpecs(configRoot)
			if err == nil {
				t.Fatal("LoadAgentSpecs() error = nil, want invalid network config error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("LoadAgentSpecs() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadAgentSpecsRejectsUnsafeAgentID(t *testing.T) {
	t.Parallel()

	configRoot := t.TempDir()
	writeAgentSpec(t, configRoot, "default", "unsafe-agent", `
version: 1
agent_id: ../unsafe
model:
  name: gpt-5
prompt: hello
`)

	_, err := LoadAgentSpecs(configRoot)
	if err == nil {
		t.Fatal("LoadAgentSpecs() error = nil, want invalid agent id error")
	}
	if !strings.Contains(err.Error(), "agent id") {
		t.Fatalf("LoadAgentSpecs() error = %v, want agent id validation error", err)
	}
}

func TestLoadAgentSpecsRejectsUnsafeDerivedAgentID(t *testing.T) {
	t.Parallel()

	configRoot := t.TempDir()
	writeAgentSpec(t, configRoot, "default", "bad agent", `
version: 1
model:
  name: gpt-5
prompt: hello
`)

	_, err := LoadAgentSpecs(configRoot)
	if err == nil {
		t.Fatal("LoadAgentSpecs() error = nil, want invalid derived agent id error")
	}
	if !strings.Contains(err.Error(), "agent id") {
		t.Fatalf("LoadAgentSpecs() error = %v, want agent id validation error", err)
	}
}

func TestLoadAgentSpecsRejectsDuplicateScheduleIDsAcrossNamespace(t *testing.T) {
	t.Parallel()

	configRoot := t.TempDir()
	writeAgentSpec(t, configRoot, "default", "worker-a", `
version: 1
model:
  name: gpt-5
prompt: hello
schedules:
  - id: every-minute
    cron: "* * * * *"
`)
	writeAgentSpec(t, configRoot, "default", "worker-b", `
version: 1
model:
  name: gpt-5
prompt: hello
schedules:
  - id: every-minute
    cron: "*/5 * * * *"
`)

	_, err := LoadAgentSpecs(configRoot)
	if err == nil {
		t.Fatal("LoadAgentSpecs() error = nil, want duplicate schedule id error")
	}
	if !strings.Contains(err.Error(), "duplicate schedule id") {
		t.Fatalf("LoadAgentSpecs() error = %v, want duplicate schedule id error", err)
	}
}
