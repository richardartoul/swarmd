package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/richardartoul/swarmd/pkg/agent"
	agentanthropic "github.com/richardartoul/swarmd/pkg/agent/anthropic"
	agentopenai "github.com/richardartoul/swarmd/pkg/agent/openai"
	"github.com/richardartoul/swarmd/pkg/server"
	"github.com/richardartoul/swarmd/pkg/sh/interp"
	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
	toolregistry "github.com/richardartoul/swarmd/pkg/tools/registry"
)

const (
	providerAnthropic = "anthropic"
	providerOpenAI    = "openai"

	defaultRunMaxSteps    = 40
	defaultRunStepTimeout = 2 * time.Minute
)

type agentExecOptions struct {
	configPath    string
	rootDir       string
	modelOverride string
	maxSteps      int
	stepTimeout   time.Duration
	promptSuffix  string
	stripTools    []string
	memoryDir     string
	jsonSteps     bool
	tempRoot      bool
}

func configuredToolsFromSpec(spec server.AgentSpec) []agent.ConfiguredTool {
	configured := make([]agent.ConfiguredTool, 0, len(spec.Tools))
	for _, tool := range spec.Tools {
		if tool.Enabled != nil && !*tool.Enabled {
			continue
		}
		configured = append(configured, agent.ConfiguredTool{
			ID:     tool.ID,
			Config: tool.Config,
		})
	}
	if len(configured) == 0 {
		return nil
	}
	return configured
}

func stripConfiguredTools(tools []agent.ConfiguredTool, strip []string) []agent.ConfiguredTool {
	if len(strip) == 0 {
		return tools
	}
	remove := make(map[string]struct{}, len(strip))
	for _, id := range strip {
		id = strings.TrimSpace(id)
		if id != "" {
			remove[id] = struct{}{}
		}
	}
	if len(remove) == 0 {
		return tools
	}
	out := make([]agent.ConfiguredTool, 0, len(tools))
	for _, t := range tools {
		if _, ok := remove[t.ID]; ok {
			continue
		}
		out = append(out, t)
	}
	return out
}

func parseStripToolsFlag(values []string) []string {
	var out []string
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func secretPreflight(tools []agent.ConfiguredTool, lookupEnv func(string) string, stdout io.Writer) []string {
	type row struct {
		tool    string
		env     string
		present bool
	}
	var rows []row
	missingSet := map[string]struct{}{}
	for _, t := range tools {
		required := toolregistry.RequiredEnvForTool(t.ID)
		if len(required) == 0 {
			rows = append(rows, row{tool: t.ID, env: "(none)", present: true})
			continue
		}
		for _, env := range required {
			present := lookupEnv(env) != ""
			rows = append(rows, row{tool: t.ID, env: env, present: present})
			if !present {
				missingSet[fmt.Sprintf("%s(%s)", t.ID, env)] = struct{}{}
			}
		}
	}
	fmt.Fprintln(stdout, "secret preflight:")
	for _, r := range rows {
		mark := "ok"
		if !r.present {
			mark = "MISSING"
		}
		fmt.Fprintf(stdout, "  %-22s needs %-32s %s\n", r.tool, r.env, mark)
	}
	missing := make([]string, 0, len(missingSet))
	for k := range missingSet {
		missing = append(missing, k)
	}
	sort.Strings(missing)
	return missing
}

func newDriverForSpec(provider, model string, lookupEnv func(string) string) (agent.Driver, error) {
	provider = strings.TrimSpace(strings.ToLower(provider))
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, fmt.Errorf("model.name must not be empty")
	}
	if provider == "" {
		if strings.HasPrefix(model, "claude-") {
			provider = providerAnthropic
		} else {
			provider = providerOpenAI
		}
	}
	switch provider {
	case providerAnthropic:
		apiKey := lookupEnv("ANTHROPIC_API_KEY")
		if apiKey == "" {
			return nil, fmt.Errorf("ANTHROPIC_API_KEY is required for anthropic models")
		}
		return agentanthropic.New(agentanthropic.Config{
			APIKey:  apiKey,
			BaseURL: lookupEnv("ANTHROPIC_BASE_URL"),
			Model:   model,
		})
	case providerOpenAI:
		apiKey := lookupEnv("OPENAI_API_KEY")
		if apiKey == "" {
			return nil, fmt.Errorf("OPENAI_API_KEY is required for openai models")
		}
		return agentopenai.New(agentopenai.Config{
			APIKey:  apiKey,
			BaseURL: lookupEnv("OPENAI_BASE_URL"),
			Model:   model,
		})
	default:
		return nil, fmt.Errorf("unsupported model provider %q", provider)
	}
}

type runToolRuntime struct {
	namespaceID string
	agentID     string
	lookupEnv   func(string) string
}

func newRunToolRuntime(namespaceID, agentID string, lookupEnv func(string) string) server.ToolRuntime {
	return runToolRuntime{namespaceID: namespaceID, agentID: agentID, lookupEnv: lookupEnv}
}

func (r runToolRuntime) NamespaceID() string { return r.namespaceID }
func (r runToolRuntime) AgentID() string     { return r.agentID }
func (r runToolRuntime) LookupEnv(name string) string {
	if r.lookupEnv == nil {
		return ""
	}
	return r.lookupEnv(name)
}
func (r runToolRuntime) Logger() server.ToolLogger { return nil }

func envLookup() func(string) string {
	return func(name string) string {
		return strings.TrimSpace(os.Getenv(strings.TrimSpace(name)))
	}
}

func resolveMaxSteps(spec server.AgentSpec, override int) int {
	if override > 0 {
		return override
	}
	if spec.Runtime.MaxSteps > 0 {
		return spec.Runtime.MaxSteps
	}
	return defaultRunMaxSteps
}

func resolveStepTimeout(spec server.AgentSpec, override time.Duration) time.Duration {
	if override > 0 {
		return override
	}
	if raw := strings.TrimSpace(spec.Runtime.StepTimeout); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil {
			return parsed
		}
	}
	return defaultRunStepTimeout
}

func composePrompt(spec server.AgentSpec, suffix string) string {
	prompt := agent.ComposeSystemPrompt(spec.Prompt)
	suffix = strings.TrimSpace(suffix)
	if suffix == "" {
		return prompt
	}
	if prompt == "" {
		return suffix
	}
	return prompt + "\n\n" + suffix
}

func executeAgent(ctx context.Context, spec server.AgentSpec, opts agentExecOptions, streams commandIO) error {
	rootDir := opts.rootDir
	var cleanup func()
	if opts.tempRoot {
		tmp, err := os.MkdirTemp("", "swarmd-agent-sandbox-")
		if err != nil {
			return fmt.Errorf("create temp root: %w", err)
		}
		rootDir = tmp
		cleanup = func() { _ = os.RemoveAll(tmp) }
		defer cleanup()
	} else if rootDir == "" {
		tmp, err := os.MkdirTemp("", "swarmd-agent-run-")
		if err != nil {
			return fmt.Errorf("create temp root: %w", err)
		}
		rootDir = tmp
	}

	sandboxMemoryDir := filepath.Join(rootDir, ".memory")
	if err := os.MkdirAll(sandboxMemoryDir, 0o755); err != nil {
		return fmt.Errorf("create .memory dir: %w", err)
	}
	if opts.memoryDir != "" {
		src, err := resolveMemoryDir(opts.memoryDir)
		if err != nil {
			return err
		}
		if err := loadMemoryState(src, sandboxMemoryDir); err != nil {
			return fmt.Errorf("load memory from %q: %w", src, err)
		}
	}

	lookupEnv := envLookup()
	configuredTools := stripConfiguredTools(configuredToolsFromSpec(spec), opts.stripTools)
	missing := secretPreflight(configuredTools, lookupEnv, streams.stdout)
	if len(missing) > 0 {
		return fmt.Errorf("secret preflight failed: missing required env for tools: %s", strings.Join(missing, ", "))
	}

	model := spec.Model.Name
	if opts.modelOverride != "" {
		model = opts.modelOverride
	}
	driver, err := newDriverForSpec(spec.Model.Provider, model, lookupEnv)
	if err != nil {
		return err
	}

	fsys, err := sandbox.NewFS(rootDir)
	if err != nil {
		return fmt.Errorf("create sandbox fs: %w", err)
	}

	var onStep agent.StepHandler
	var onResult agent.ResultHandler
	if opts.jsonSteps {
		emitter := newJSONStepsEmitter(streams.stdout)
		onStep = emitter
		onResult = emitter
	}

	cfg := agent.Config{
		FileSystem:           fsys,
		NetworkDialer:        interp.OSNetworkDialer{},
		GlobalReachableHosts: []interp.HostMatcher{{Glob: "*"}},
		ConfiguredTools:      configuredTools,
		ToolRuntimeData:      newRunToolRuntime(spec.NamespaceID, spec.AgentID, lookupEnv),
		Driver:               driver,
		OnStep:               onStep,
		OnResult:             onResult,
		MaxSteps:             resolveMaxSteps(spec, opts.maxSteps),
		StepTimeout:          resolveStepTimeout(spec, opts.stepTimeout),
		MaxOutputBytes:       spec.Runtime.MaxOutputBytes,
		SystemPrompt:         composePrompt(spec, opts.promptSuffix),
		Stdout:               streams.stdout,
		Stderr:               streams.stderr,
	}

	runtime, err := agent.New(cfg)
	if err != nil {
		return err
	}
	defer runtime.Close()

	trigger := agent.Trigger{
		ID:         fmt.Sprintf("agent-run-%d", time.Now().UnixNano()),
		Kind:       "manual_run",
		Payload:    fmt.Sprintf("Run the %q agent now.", spec.AgentID),
		EnqueuedAt: time.Now(),
	}

	result, err := agent.NewSession(runtime).RunTrigger(ctx, trigger)
	if err != nil {
		return err
	}
	if result.Status != agent.ResultStatusFinished {
		return fmt.Errorf("agent run did not finish: status=%s error=%s", result.Status, result.Error)
	}

	if opts.memoryDir != "" {
		dest, err := resolveMemoryDir(opts.memoryDir)
		if err != nil {
			return err
		}
		if err := writebackMemoryState(sandboxMemoryDir, dest); err != nil {
			return fmt.Errorf("memory writeback to %q: %w", dest, err)
		}
	}
	return nil
}
