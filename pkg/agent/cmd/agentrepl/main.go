package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/richardartoul/swarmd/pkg/agent"
	agentanthropic "github.com/richardartoul/swarmd/pkg/agent/anthropic"
	agentopenai "github.com/richardartoul/swarmd/pkg/agent/openai"
	"github.com/richardartoul/swarmd/pkg/server"
	"github.com/richardartoul/swarmd/pkg/sh/interp"
	"github.com/richardartoul/swarmd/pkg/sh/memfs"
	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
	_ "github.com/richardartoul/swarmd/pkg/tools/customtools"
	toolregistry "github.com/richardartoul/swarmd/pkg/tools/registry"
)

var (
	provider             = flag.String("provider", "", `model provider: "openai" or "anthropic". Empty auto-detects from flags and environment.`)
	apiKey               = flag.String("api-key", "", "LLM API key. Defaults to OPENAI_API_KEY or ANTHROPIC_API_KEY depending on the selected provider.")
	baseURL              = flag.String("base-url", "", "LLM API base URL. Defaults to OPENAI_BASE_URL or ANTHROPIC_BASE_URL depending on the selected provider.")
	model                = flag.String("model", "", "LLM model name. Defaults to OPENAI_MODEL or ANTHROPIC_MODEL depending on the selected provider. OpenAI suffixes like -xnone, -xlow, -xmedium, and -xhigh infer reasoning effort; Anthropic suffixes like -low, -medium, -high, and -max do the same.")
	promptCacheKey       = flag.String("prompt-cache-key", "", "OpenAI only: prompt cache routing key. Empty lets agentrepl choose a default for the OpenAI API.")
	promptCacheRetention = flag.String("prompt-cache-retention", "", "OpenAI only: prompt cache retention policy. Supported values are in_memory and 24h. Empty lets agentrepl choose a default for supported OpenAI models.")
	rootDir              = flag.String("root", ".", "sandbox root directory, or logical root when -memfs is set")
	useMemFS             = flag.Bool("memfs", false, "use an in-memory filesystem backend")
	allowNetwork         = flag.Bool("allow-network", true, "allow in-process shell commands like curl to use the host network dialer")
	systemPromptFile     = flag.String("system-prompt-file", "", "optional file containing agent-specific instructions appended to the default system prompt")
	prompt               = flag.String("prompt", "", "optional single prompt to run before exiting")

	maxSteps       = flag.Int("max-steps", defaultREPLMaxSteps, "maximum action steps per prompt")
	stepTimeout    = flag.Duration("step-timeout", defaultREPLStepTimeout, "timeout for one action step")
	maxOutputBytes = flag.Int("max-output-bytes", agent.DefaultMaxOutputBytes, "maximum captured bytes per stream per step")

	preserveState = flag.Bool("preserve-state", true, "preserve shell state between prompts")
	verbose       = flag.Bool("verbose", true, "show per-step thought or provider reasoning summary, chosen shell command, and step completion lines")
)

const (
	defaultREPLMaxSteps    = 1000
	defaultREPLStepTimeout = 5 * time.Minute

	replProviderOpenAI    = "openai"
	replProviderAnthropic = "anthropic"
)

func main() {
	flag.Parse()

	if err := runAll(); err != nil {
		if errors.Is(err, io.EOF) {
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type runtimeOptions struct {
	rootDir        string
	useMemFS       bool
	networkDialer  interp.NetworkDialer
	lookupEnv      func(string) string
	systemPrompt   string
	baseDriver     agent.Driver
	modelName      string
	singlePrompt   string
	maxSteps       int
	stepTimeout    time.Duration
	maxOutputBytes int
	preserveState  bool
	verbose        bool
}

type runtimeFlagValues struct {
	provider             string
	apiKey               string
	baseURL              string
	model                string
	promptCacheKey       string
	promptCacheRetention string
	rootDir              string
	useMemFS             bool
	allowNetwork         bool
	systemPromptFile     string
	prompt               string
	maxSteps             int
	stepTimeout          time.Duration
	maxOutputBytes       int
	preserveState        bool
	verbose              bool
}

type resolvedProvider struct {
	name       string
	apiKeyEnv  string
	baseURLEnv string
	modelEnv   string
	apiKey     string
	baseURL    string
	model      string
}

func runAll() error {
	opts, err := buildRuntimeOptions()
	if err != nil {
		return err
	}

	stdinTTY := term.IsTerminal(int(os.Stdin.Fd()))
	stdoutTTY := term.IsTerminal(int(os.Stdout.Fd()))
	if shouldUseTUI(opts.singlePrompt, stdinTTY, stdoutTTY) {
		return runTUICommand(context.Background(), opts)
	}
	return runPlainCommand(context.Background(), opts)
}

func buildRuntimeOptions() (runtimeOptions, error) {
	return buildRuntimeOptionsFromValues(runtimeFlagValues{
		provider:             *provider,
		apiKey:               *apiKey,
		baseURL:              *baseURL,
		model:                *model,
		promptCacheKey:       *promptCacheKey,
		promptCacheRetention: *promptCacheRetention,
		rootDir:              *rootDir,
		useMemFS:             *useMemFS,
		allowNetwork:         *allowNetwork,
		systemPromptFile:     *systemPromptFile,
		prompt:               *prompt,
		maxSteps:             *maxSteps,
		stepTimeout:          *stepTimeout,
		maxOutputBytes:       *maxOutputBytes,
		preserveState:        *preserveState,
		verbose:              *verbose,
	}, os.Getenv)
}

func buildRuntimeOptionsFromValues(values runtimeFlagValues, lookupEnv func(string) string) (runtimeOptions, error) {
	selectedProvider, err := resolveREPLProvider(values, lookupEnv)
	if err != nil {
		return runtimeOptions{}, err
	}

	var systemPrompt string
	if values.systemPromptFile != "" {
		data, err := os.ReadFile(values.systemPromptFile)
		if err != nil {
			return runtimeOptions{}, err
		}
		systemPrompt = string(data)
	}

	var networkDialer interp.NetworkDialer
	if values.allowNetwork {
		networkDialer = interp.OSNetworkDialer{}
	}

	baseDriver, err := newREPLBaseDriver(selectedProvider, values.promptCacheKey, values.promptCacheRetention)
	if err != nil {
		return runtimeOptions{}, err
	}

	return runtimeOptions{
		rootDir:        values.rootDir,
		useMemFS:       values.useMemFS,
		networkDialer:  networkDialer,
		lookupEnv:      os.Getenv,
		systemPrompt:   systemPrompt,
		baseDriver:     baseDriver,
		modelName:      selectedProvider.model,
		singlePrompt:   values.prompt,
		maxSteps:       values.maxSteps,
		stepTimeout:    values.stepTimeout,
		maxOutputBytes: values.maxOutputBytes,
		preserveState:  values.preserveState,
		verbose:        values.verbose,
	}, nil
}

func resolveREPLProvider(values runtimeFlagValues, lookupEnv func(string) string) (resolvedProvider, error) {
	lookupEnv = trimmedEnvLookup(lookupEnv)
	providerName := strings.TrimSpace(values.provider)
	if providerName == "" {
		providerName = detectREPLProvider(values, lookupEnv)
	}
	selectedProvider, err := newResolvedProvider(providerName)
	if err != nil {
		return resolvedProvider{}, err
	}
	selectedProvider.apiKey = firstNonEmpty(values.apiKey, lookupEnv(selectedProvider.apiKeyEnv))
	selectedProvider.baseURL = firstNonEmpty(values.baseURL, lookupEnv(selectedProvider.baseURLEnv))
	selectedProvider.model = firstNonEmpty(values.model, lookupEnv(selectedProvider.modelEnv))
	if selectedProvider.apiKey == "" {
		if strings.TrimSpace(values.apiKey) == "" && lookupEnv("OPENAI_API_KEY") == "" && lookupEnv("ANTHROPIC_API_KEY") == "" {
			return resolvedProvider{}, fmt.Errorf("llm api key is required; pass -api-key or set OPENAI_API_KEY or ANTHROPIC_API_KEY")
		}
		return resolvedProvider{}, fmt.Errorf("%s api key is required; pass -api-key or set %s", selectedProvider.name, selectedProvider.apiKeyEnv)
	}
	if selectedProvider.model == "" {
		return resolvedProvider{}, fmt.Errorf("%s model is required; pass -model or set %s", selectedProvider.name, selectedProvider.modelEnv)
	}
	return selectedProvider, nil
}

func newResolvedProvider(name string) (resolvedProvider, error) {
	switch strings.TrimSpace(name) {
	case replProviderOpenAI:
		return resolvedProvider{
			name:       replProviderOpenAI,
			apiKeyEnv:  "OPENAI_API_KEY",
			baseURLEnv: "OPENAI_BASE_URL",
			modelEnv:   "OPENAI_MODEL",
		}, nil
	case replProviderAnthropic:
		return resolvedProvider{
			name:       replProviderAnthropic,
			apiKeyEnv:  "ANTHROPIC_API_KEY",
			baseURLEnv: "ANTHROPIC_BASE_URL",
			modelEnv:   "ANTHROPIC_MODEL",
		}, nil
	default:
		return resolvedProvider{}, fmt.Errorf(`-provider must be empty, "openai", or "anthropic"`)
	}
}

func detectREPLProvider(values runtimeFlagValues, lookupEnv func(string) string) string {
	if providerName := providerFromModel(values.model); providerName != "" {
		return providerName
	}
	if providerName := providerFromBaseURL(values.baseURL); providerName != "" {
		return providerName
	}
	switch openAIAPIKey, anthropicAPIKey := lookupEnv("OPENAI_API_KEY"), lookupEnv("ANTHROPIC_API_KEY"); {
	case openAIAPIKey == "" && anthropicAPIKey != "":
		return replProviderAnthropic
	case anthropicAPIKey == "" && openAIAPIKey != "":
		return replProviderOpenAI
	}
	switch openAIModel, anthropicModel := lookupEnv("OPENAI_MODEL"), lookupEnv("ANTHROPIC_MODEL"); {
	case openAIModel == "" && anthropicModel != "":
		return replProviderAnthropic
	case anthropicModel == "" && openAIModel != "":
		return replProviderOpenAI
	}
	switch openAIBaseURL, anthropicBaseURL := lookupEnv("OPENAI_BASE_URL"), lookupEnv("ANTHROPIC_BASE_URL"); {
	case openAIBaseURL == "" && anthropicBaseURL != "":
		return replProviderAnthropic
	case anthropicBaseURL == "" && openAIBaseURL != "":
		return replProviderOpenAI
	default:
		return replProviderOpenAI
	}
}

func providerFromModel(model string) string {
	model = strings.TrimSpace(model)
	switch {
	case strings.HasPrefix(model, "claude-"):
		return replProviderAnthropic
	case strings.HasPrefix(model, "chatgpt-"),
		strings.HasPrefix(model, "gpt-"),
		strings.HasPrefix(model, "o1"),
		strings.HasPrefix(model, "o3"),
		strings.HasPrefix(model, "o4"):
		return replProviderOpenAI
	default:
		return ""
	}
}

func providerFromBaseURL(baseURL string) string {
	switch strings.TrimRight(strings.TrimSpace(baseURL), "/") {
	case agentopenai.DefaultBaseURL:
		return replProviderOpenAI
	case agentanthropic.DefaultBaseURL:
		return replProviderAnthropic
	default:
		return ""
	}
}

func newREPLBaseDriver(selectedProvider resolvedProvider, promptCacheKey, promptCacheRetention string) (agent.Driver, error) {
	switch selectedProvider.name {
	case replProviderOpenAI:
		cacheKey := strings.TrimSpace(promptCacheKey)
		cacheRetention := strings.TrimSpace(promptCacheRetention)
		if normalizeOpenAIBaseURL(selectedProvider.baseURL) == agentopenai.DefaultBaseURL {
			if cacheKey == "" {
				cacheKey = "agentrepl:" + baseModel(selectedProvider.model)
			}
			if cacheRetention == "" && supportsExtendedPromptCache(baseModel(selectedProvider.model)) {
				cacheRetention = "24h"
			}
		}
		return agentopenai.New(agentopenai.Config{
			APIKey:               selectedProvider.apiKey,
			BaseURL:              selectedProvider.baseURL,
			Model:                selectedProvider.model,
			PromptCacheKey:       cacheKey,
			PromptCacheRetention: cacheRetention,
		})
	case replProviderAnthropic:
		return agentanthropic.New(agentanthropic.Config{
			APIKey:  selectedProvider.apiKey,
			BaseURL: selectedProvider.baseURL,
			Model:   selectedProvider.model,
		})
	default:
		return nil, fmt.Errorf("unsupported repl provider %q", selectedProvider.name)
	}
}

func shouldUseTUI(prompt string, stdinTTY, stdoutTTY bool) bool {
	return strings.TrimSpace(prompt) == "" && stdinTTY && stdoutTTY
}

func (opts runtimeOptions) agentConfig(
	queue agent.Queue,
	driver agent.Driver,
	onStep agent.StepHandler,
	onResult agent.ResultHandler,
	stdout io.Writer,
	stderr io.Writer,
) (agent.Config, error) {
	var (
		fsys sandbox.FileSystem
		err  error
	)
	if opts.useMemFS {
		fsys, err = memfs.New(opts.rootDir)
	} else {
		fsys, err = sandbox.NewFS(opts.rootDir)
	}
	if err != nil {
		return agent.Config{}, err
	}
	var globalReachableHosts []interp.HostMatcher
	if opts.networkDialer != nil {
		globalReachableHosts = []interp.HostMatcher{{Glob: "*"}}
	}
	configuredTools := opts.autoConfiguredTools()
	cfg := agent.Config{
		FileSystem:                   fsys,
		NetworkDialer:                opts.networkDialer,
		GlobalReachableHosts:         globalReachableHosts,
		ConfiguredTools:              configuredTools,
		ToolRuntimeData:              newREPLToolRuntime(opts.toolEnvLookup()),
		Queue:                        queue,
		Driver:                       driver,
		OnStep:                       onStep,
		OnResult:                     onResult,
		MaxSteps:                     opts.maxSteps,
		StepTimeout:                  opts.stepTimeout,
		MaxOutputBytes:               opts.maxOutputBytes,
		PreserveStateBetweenTriggers: opts.preserveState,
		SystemPrompt:                 agent.ComposeSystemPrompt(opts.systemPrompt),
		Stdout:                       stdout,
		Stderr:                       stderr,
	}
	return cfg, nil
}

func (opts runtimeOptions) autoConfiguredTools() []agent.ConfiguredTool {
	lookupEnv := opts.toolEnvLookup()
	networkEnabled := opts.networkDialer != nil
	configured := make([]agent.ConfiguredTool, 0)
	for _, tool := range toolregistry.RegisteredCustomTools() {
		if !shouldAutoEnableREPLTool(tool, lookupEnv, networkEnabled) {
			continue
		}
		configured = append(configured, agent.ConfiguredTool{ID: tool.Definition.Name})
	}
	if len(configured) == 0 {
		return nil
	}
	return configured
}

func shouldAutoEnableREPLTool(tool toolregistry.CustomToolCatalogEntry, lookupEnv func(string) string, networkEnabled bool) bool {
	// v1 only auto-enables credential-backed custom tools.
	if len(tool.RequiredEnv) == 0 {
		return false
	}
	if tool.Definition.NetworkScope.Normalized() != agent.ToolNetworkScopeNone && !networkEnabled {
		return false
	}
	for _, name := range tool.RequiredEnv {
		if strings.TrimSpace(lookupEnv(name)) == "" {
			return false
		}
	}
	return true
}

func (opts runtimeOptions) toolEnvLookup() func(string) string {
	if opts.lookupEnv == nil {
		return func(name string) string {
			return strings.TrimSpace(os.Getenv(strings.TrimSpace(name)))
		}
	}
	return func(name string) string {
		return strings.TrimSpace(opts.lookupEnv(strings.TrimSpace(name)))
	}
}

type replToolRuntime struct {
	lookupEnv func(string) string
}

func newREPLToolRuntime(lookupEnv func(string) string) server.ToolRuntime {
	return replToolRuntime{lookupEnv: lookupEnv}
}

func (r replToolRuntime) NamespaceID() string {
	return "agentrepl"
}

func (r replToolRuntime) AgentID() string {
	return "agentrepl"
}

func (r replToolRuntime) LookupEnv(name string) string {
	if r.lookupEnv == nil {
		return ""
	}
	return strings.TrimSpace(r.lookupEnv(strings.TrimSpace(name)))
}

func (r replToolRuntime) Logger() server.ToolLogger {
	return nil
}

type replQueue struct {
	reader      *bufio.Reader
	stdout      io.Writer
	interactive bool
	nextID      int
}

func newREPLQueue(stdin *os.File, stdout io.Writer) *replQueue {
	return &replQueue{
		reader:      bufio.NewReader(stdin),
		stdout:      stdout,
		interactive: term.IsTerminal(int(stdin.Fd())),
	}
}

func (q *replQueue) Next(ctx context.Context) (agent.Trigger, error) {
	_ = ctx

	for {
		if q.interactive {
			fmt.Fprint(q.stdout, "agent> ")
		}

		line, err := q.reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return agent.Trigger{}, err
		}

		line = strings.TrimSpace(line)
		switch line {
		case "":
			if errors.Is(err, io.EOF) {
				return agent.Trigger{}, io.EOF
			}
			continue
		case ":quit", ":exit":
			return agent.Trigger{}, io.EOF
		case ":help":
			fmt.Fprintln(q.stdout, "Enter a prompt to send it to the agent. Use :quit to exit.")
			if errors.Is(err, io.EOF) {
				return agent.Trigger{}, io.EOF
			}
			continue
		}

		q.nextID++
		return makeTrigger(line, q.nextID), nil
	}
}

type singlePromptQueue struct {
	trigger agent.Trigger
	used    bool
}

func (q *singlePromptQueue) Next(ctx context.Context) (agent.Trigger, error) {
	_ = ctx
	if q.used {
		return agent.Trigger{}, io.EOF
	}
	q.used = true
	return q.trigger, nil
}

type verboseDriver struct {
	next   agent.Driver
	stdout io.Writer
}

func (d verboseDriver) Next(ctx context.Context, req agent.Request) (agent.Decision, error) {
	decision, err := d.next.Next(ctx, req)
	if err != nil {
		return agent.Decision{}, err
	}
	if decision.Thought != "" {
		fmt.Fprintln(d.stdout)
		fmt.Fprint(d.stdout, prefixLines(fmt.Sprintf("step %d thinking> ", req.Step), decision.Thought))
		fmt.Fprintln(d.stdout)
	}
	if decision.Tool != nil {
		label := decision.Tool.Name
		if label == "" {
			label = "tool"
		}
		fmt.Fprint(d.stdout, prefixLines(fmt.Sprintf("step %d tool> ", req.Step), label))
		fmt.Fprintln(d.stdout)
		if strings.TrimSpace(decision.Tool.Input) != "" {
			fmt.Fprint(d.stdout, prefixLines(fmt.Sprintf("step %d input> ", req.Step), decision.Tool.Input))
			fmt.Fprintln(d.stdout)
		}
	}
	return decision, nil
}

type progressPrinter struct {
	stdout  io.Writer
	stderr  io.Writer
	verbose bool
}

func (p progressPrinter) HandleStep(ctx context.Context, trigger agent.Trigger, step agent.Step) error {
	_ = ctx
	_ = trigger

	if !p.verbose {
		return nil
	}

	fmt.Fprintln(p.stdout)
	if output := renderStepActionOutput(step); output != "" {
		fmt.Fprint(p.stdout, prefixLines(fmt.Sprintf("step %d output> ", step.Index), output))
		fmt.Fprintln(p.stdout)
	}
	switch step.Status {
	case agent.StepStatusExitStatus:
		fmt.Fprintf(p.stdout, "step %d done [%s, exit=%d]\n", step.Index, step.Status, step.ExitStatus)
	default:
		fmt.Fprintf(p.stdout, "step %d done [%s]\n", step.Index, step.Status)
	}
	if step.Error != "" {
		fmt.Fprint(p.stderr, prefixLines(fmt.Sprintf("step %d error> ", step.Index), step.Error))
		fmt.Fprintln(p.stderr)
	}
	return nil
}

func (p progressPrinter) HandleResult(ctx context.Context, result agent.Result) error {
	_ = ctx

	fmt.Fprintln(p.stdout)
	if !p.verbose {
		for _, step := range result.Steps {
			if step.Status == agent.StepStatusOK {
				continue
			}
			fmt.Fprintf(p.stderr, "step %d [%s]: %s\n", step.Index, step.Status, step.Error)
		}
	}

	switch result.Status {
	case agent.ResultStatusFinished:
		fmt.Fprint(p.stdout, prefixLines("assistant> ", agent.RenderResultValue(result.Value)))
		fmt.Fprintln(p.stdout)
	default:
		if result.Error != "" {
			fmt.Fprintf(p.stderr, "result [%s]: %s\n", result.Status, result.Error)
		} else {
			fmt.Fprintf(p.stderr, "result [%s]\n", result.Status)
		}
	}
	fmt.Fprintf(p.stdout, "usage> cached prompt tokens: %d\n", result.Usage.CachedTokens)

	if len(result.Steps) > 0 {
		fmt.Fprintln(p.stdout)
	}
	return nil
}

func makeTrigger(prompt string, id int) agent.Trigger {
	return agent.Trigger{
		ID:         fmt.Sprintf("prompt-%d", id),
		Kind:       "repl.prompt",
		Payload:    prompt,
		EnqueuedAt: time.Now(),
	}
}

func runSessionLoop(ctx context.Context, queue agent.Queue, session *agent.Session) error {
	for {
		trigger, err := queue.Next(ctx)
		if err != nil {
			return err
		}
		if _, err := session.RunTrigger(ctx, trigger); err != nil {
			return err
		}
	}
}

func renderStepActionOutput(step agent.Step) string {
	output := strings.TrimRight(step.ActionOutput, "\n")
	if output == "" {
		return ""
	}
	if step.ActionOutputTruncated {
		output += "\n[output truncated]"
	}
	return output
}

func prefixLines(prefix, text string) string {
	text = strings.TrimRight(text, "\n")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

func trimmedEnvLookup(lookupEnv func(string) string) func(string) string {
	if lookupEnv == nil {
		return func(string) string { return "" }
	}
	return func(name string) string {
		return strings.TrimSpace(lookupEnv(strings.TrimSpace(name)))
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
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
