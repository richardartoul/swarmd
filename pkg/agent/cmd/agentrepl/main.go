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
	agentopenai "github.com/richardartoul/swarmd/pkg/agent/openai"
	"github.com/richardartoul/swarmd/pkg/server"
	"github.com/richardartoul/swarmd/pkg/sh/interp"
	"github.com/richardartoul/swarmd/pkg/sh/memfs"
	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
	_ "github.com/richardartoul/swarmd/pkg/tools/customtools"
	toolregistry "github.com/richardartoul/swarmd/pkg/tools/registry"
)

var (
	apiKey               = flag.String("api-key", os.Getenv("OPENAI_API_KEY"), "OpenAI API key (or OPENAI_API_KEY)")
	baseURL              = flag.String("base-url", envOr("OPENAI_BASE_URL", agentopenai.DefaultBaseURL), "OpenAI API base URL (or OPENAI_BASE_URL)")
	model                = flag.String("model", os.Getenv("OPENAI_MODEL"), "OpenAI model name (or OPENAI_MODEL). Suffixes like -xnone, -xlow, -xmedium, and -xhigh infer reasoning effort. Supported reasoning models request reasoning summaries automatically.")
	promptCacheKey       = flag.String("prompt-cache-key", "", "OpenAI prompt cache routing key. Empty lets agentrepl choose a default for the OpenAI API.")
	promptCacheRetention = flag.String("prompt-cache-retention", "", "OpenAI prompt cache retention policy. Supported values are in_memory and 24h. Empty lets agentrepl choose a default for supported OpenAI models.")
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
	if strings.TrimSpace(*apiKey) == "" {
		return runtimeOptions{}, fmt.Errorf("openai api key is required; pass -api-key or set OPENAI_API_KEY")
	}
	if strings.TrimSpace(*model) == "" {
		return runtimeOptions{}, fmt.Errorf("openai model is required; pass -model or set OPENAI_MODEL")
	}

	var systemPrompt string
	if *systemPromptFile != "" {
		data, err := os.ReadFile(*systemPromptFile)
		if err != nil {
			return runtimeOptions{}, err
		}
		systemPrompt = string(data)
	}

	var networkDialer interp.NetworkDialer
	if *allowNetwork {
		networkDialer = interp.OSNetworkDialer{}
	}

	cacheKey := strings.TrimSpace(*promptCacheKey)
	cacheRetention := strings.TrimSpace(*promptCacheRetention)
	if normalizeBaseURL(*baseURL) == agentopenai.DefaultBaseURL {
		if cacheKey == "" {
			cacheKey = "agentrepl:" + baseModel(*model)
		}
		if cacheRetention == "" && supportsExtendedPromptCache(baseModel(*model)) {
			cacheRetention = "24h"
		}
	}

	openaiDriver, err := agentopenai.New(agentopenai.Config{
		APIKey:               *apiKey,
		BaseURL:              *baseURL,
		Model:                *model,
		PromptCacheKey:       cacheKey,
		PromptCacheRetention: cacheRetention,
	})
	if err != nil {
		return runtimeOptions{}, err
	}

	return runtimeOptions{
		rootDir:        *rootDir,
		useMemFS:       *useMemFS,
		networkDialer:  networkDialer,
		lookupEnv:      os.Getenv,
		systemPrompt:   systemPrompt,
		baseDriver:     openaiDriver,
		modelName:      *model,
		singlePrompt:   *prompt,
		maxSteps:       *maxSteps,
		stepTimeout:    *stepTimeout,
		maxOutputBytes: *maxOutputBytes,
		preserveState:  *preserveState,
		verbose:        *verbose,
	}, nil
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

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func normalizeBaseURL(baseURL string) string {
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
