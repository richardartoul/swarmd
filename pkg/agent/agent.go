// See LICENSE for licensing information

package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/richardartoul/swarmd/pkg/sh/interp"
	"github.com/richardartoul/swarmd/pkg/sh/moreinterp/coreutils"
	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
	"github.com/richardartoul/swarmd/pkg/sh/syntax"
	toolregistry "github.com/richardartoul/swarmd/pkg/tools/registry"
)

// ErrQueueRequired is returned when [Agent.Serve] is called without a queue.
var ErrQueueRequired = errors.New("agent queue is required for Serve")

const maxDriverRetries = 3

// Agent runs a queue-driven agent loop on top of a sandboxed shell.
type Agent struct {
	queue                    Queue
	driver                   Driver
	onResult                 ResultHandler
	onStep                   StepHandler
	parser                   *syntax.Parser
	runner                   *interp.Runner
	fileSystem               sandbox.FileSystem
	sandboxRoot              string
	globalHTTPClientFactory  interp.HTTPClientFactory
	stdout                   *switchWriter
	stderr                   *switchWriter
	maxSteps                 int
	maxOutputBytes           int
	outputFileThresholdBytes int
	stepTimeout              time.Duration
	preserveState            bool
	systemPrompt             string
	liveStdout               io.Writer
	liveStderr               io.Writer
	spillBaseDir             string
	currentRunSpillDir       string
	shellNetworkEnabled      bool
	globalReachableHosts     []interp.HostMatcher
	toolDefinitions          []ToolDefinition
	toolByName               map[string]ToolDefinition
	toolHandlerByName        map[string]ToolHandler
	toolRequiredHosts        map[string][]interp.HostMatcher
	toolHTTPClientFactories  map[string]interp.HTTPClientFactory
	toolRuntimeData          any
	webSearchBackend         WebSearchBackend
	imageDescriptionBackend  ImageDescriptionBackend
	customCommands           []sandbox.CommandInfo
}

type turnRunInput struct {
	Trigger        Trigger
	PriorSteps     []Step
	NextStepIndex  int
	ResetRunner    bool
	RequestContext driverRequestContext
}

// New constructs an [Agent].
func New(cfg Config) (*Agent, error) {
	if cfg.Driver == nil {
		return nil, fmt.Errorf("agent driver must not be nil")
	}
	policy := cfg.FileSystem
	if policy == nil {
		root := cfg.Root
		if root == "" {
			root = "."
		}
		var err error
		policy, err = sandbox.NewFS(root)
		if err != nil {
			return nil, err
		}
	}
	sandboxRoot, err := policy.Getwd()
	if err != nil {
		return nil, fmt.Errorf("resolve agent sandbox root: %w", err)
	}

	stdout := newSwitchWriter()
	stderr := newSwitchWriter()
	globalReachableHosts := slices.Clone(cfg.GlobalReachableHosts)
	globalHTTPClientFactory, err := newAgentHTTPClientFactory(cfg.NetworkDialer, globalReachableHosts, cfg.HTTPHeaders)
	if err != nil {
		return nil, fmt.Errorf("create agent global HTTP client factory: %w", err)
	}
	toolBindings, err := resolveToolBindings(cfg.ConfiguredTools, globalReachableHosts)
	if err != nil {
		return nil, fmt.Errorf("resolve agent tools: %w", err)
	}
	toolDefinitions := make([]ToolDefinition, 0, len(toolBindings))
	toolByName := make(map[string]ToolDefinition, len(toolBindings))
	toolHandlerByName := make(map[string]ToolHandler, len(toolBindings))
	toolRequiredHosts := make(map[string][]interp.HostMatcher, len(toolBindings))
	toolHTTPClientFactories := make(map[string]interp.HTTPClientFactory, len(toolBindings))
	for _, binding := range toolBindings {
		toolDefinitions = append(toolDefinitions, binding.Definition)
		toolByName[binding.Definition.Name] = binding.Definition
		toolHandlerByName[binding.Definition.Name] = binding.Handler
		requiredHosts := toolregistry.RequiredHostsForTool(binding.Definition.Name)
		if len(requiredHosts) > 0 {
			toolRequiredHosts[binding.Definition.Name] = requiredHosts
		}
		factory, err := newAgentHTTPClientFactory(
			cfg.NetworkDialer,
			effectiveToolReachableHosts(binding.Definition.NetworkScope, globalReachableHosts, requiredHosts),
			cfg.HTTPHeaders,
		)
		if err != nil {
			return nil, fmt.Errorf("create HTTP client factory for tool %q: %w", binding.Definition.Name, err)
		}
		toolHTTPClientFactories[binding.Definition.Name] = factory
	}
	webSearchBackend := cfg.WebSearchBackend
	if webSearchBackend == nil {
		webSearchBackend = NewDuckDuckGoWebSearchBackend()
	}
	imageDescriptionBackend := cfg.ImageDescriptionBackend
	if imageDescriptionBackend == nil {
		if backend, ok := cfg.Driver.(ImageDescriptionBackend); ok {
			imageDescriptionBackend = backend
		}
	}
	globalNetworkDialer, err := newAgentScopedNetworkDialer(cfg.NetworkDialer, globalReachableHosts)
	if err != nil {
		return nil, fmt.Errorf("create agent shell network dialer: %w", err)
	}
	runner, err := sandbox.NewRunnerWithConfig(policy, sandbox.RunnerConfig{
		Stdout:            stdout,
		Stderr:            stderr,
		NetworkDialer:     globalNetworkDialer,
		NetworkEnabled:    len(globalReachableHosts) > 0,
		HTTPHeaders:       cfg.HTTPHeaders,
		HTTPClientFactory: globalHTTPClientFactory,
		ProgramValidator:  validateProgram,
		CustomCommands:    cfg.CustomCommands,
		CallHandlers: []interp.CallHandlerFunc{
			blockControlBuiltins,
		},
	})
	if err != nil {
		return nil, err
	}

	maxSteps := cfg.MaxSteps
	if maxSteps <= 0 {
		maxSteps = DefaultMaxSteps
	}
	maxOutputBytes := cfg.MaxOutputBytes
	if maxOutputBytes <= 0 {
		maxOutputBytes = DefaultMaxOutputBytes
	}
	outputFileThresholdBytes := cfg.OutputFileThresholdBytes
	if outputFileThresholdBytes <= 0 {
		outputFileThresholdBytes = maxOutputBytes
	}
	spillBaseDir := outputSpillBaseDir(policy, sandboxRoot)
	systemPrompt := strings.TrimSpace(cfg.SystemPrompt)
	if systemPrompt == "" {
		systemPrompt = defaultSystemPrompt()
	}

	return &Agent{
		queue:                    cfg.Queue,
		driver:                   cfg.Driver,
		onResult:                 cfg.OnResult,
		onStep:                   cfg.OnStep,
		parser:                   syntax.NewParser(syntax.Variant(syntax.LangPOSIX)),
		runner:                   runner,
		fileSystem:               policy,
		sandboxRoot:              sandboxRoot,
		globalHTTPClientFactory:  globalHTTPClientFactory,
		stdout:                   stdout,
		stderr:                   stderr,
		maxSteps:                 maxSteps,
		maxOutputBytes:           maxOutputBytes,
		outputFileThresholdBytes: outputFileThresholdBytes,
		stepTimeout:              cfg.StepTimeout,
		preserveState:            cfg.PreserveStateBetweenTriggers,
		systemPrompt:             systemPrompt,
		liveStdout:               cfg.Stdout,
		liveStderr:               cfg.Stderr,
		spillBaseDir:             spillBaseDir,
		shellNetworkEnabled:      len(globalReachableHosts) > 0,
		globalReachableHosts:     globalReachableHosts,
		toolDefinitions:          toolDefinitions,
		toolByName:               toolByName,
		toolHandlerByName:        toolHandlerByName,
		toolRequiredHosts:        toolRequiredHosts,
		toolHTTPClientFactories:  toolHTTPClientFactories,
		toolRuntimeData:          cfg.ToolRuntimeData,
		webSearchBackend:         webSearchBackend,
		imageDescriptionBackend:  imageDescriptionBackend,
		customCommands:           commandInfosFromCustomCommands(cfg.CustomCommands),
	}, nil
}

// Serve waits on the configured queue and handles triggers sequentially.
func (a *Agent) Serve(ctx context.Context) error {
	if a.queue == nil {
		return ErrQueueRequired
	}

	for {
		trigger, err := a.queue.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}

		result, err := a.HandleTrigger(ctx, trigger)
		if err != nil {
			return err
		}
		if err := a.handleResult(ctx, result); err != nil {
			return err
		}
	}
}

// Close releases any runtime-owned temporary spill files for this agent.
func (a *Agent) Close() error {
	return a.clearSpillBaseDir()
}

// HandleTrigger handles a single trigger synchronously.
func (a *Agent) HandleTrigger(ctx context.Context, trigger Trigger) (Result, error) {
	return a.runTurn(ctx, turnRunInput{
		Trigger:        trigger,
		NextStepIndex:  1,
		ResetRunner:    !a.preserveState,
		RequestContext: newTriggerDriverRequestContext(),
	})
}

func (a *Agent) runTurn(ctx context.Context, input turnRunInput) (Result, error) {
	result := Result{
		Trigger:   input.Trigger,
		StartedAt: time.Now(),
	}
	cleanupSpill, err := a.beginRunSpillDir(input.Trigger)
	if err != nil {
		result.Status = ResultStatusFatalError
		result.Error = err.Error()
		return a.finishResult(result, input.RequestContext.withRunStartedAt(result.StartedAt)), err
	}
	defer cleanupSpill()

	if input.ResetRunner {
		a.runner.Reset()
	}

	nextStepIndex := input.NextStepIndex
	if nextStepIndex <= 0 {
		nextStepIndex = 1
	}
	requestContext := input.RequestContext.withRunStartedAt(result.StartedAt)
	turnSteps := make([]Step, 0, a.maxSteps)
	for requestStep := 1; requestStep <= a.maxSteps; requestStep++ {
		if err := ctx.Err(); err != nil {
			result.Status = ResultStatusCanceled
			result.Error = err.Error()
			result.Steps = turnSteps
			return a.finishResult(result, requestContext), err
		}

		request, _, err := a.buildDriverRequestWithContext(
			input.Trigger,
			requestStep,
			a.runner.Dir,
			turnSteps,
			requestContext,
		)
		if err != nil {
			result.Status = ResultStatusDriverError
			result.Error = err.Error()
			result.Steps = turnSteps
			return a.finishResult(result, requestContext), nil
		}

		decision, err := a.nextDecision(ctx, request)
		if err != nil {
			if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
				result.Status = ResultStatusCanceled
				result.Error = ctx.Err().Error()
				result.Steps = turnSteps
				return a.finishResult(result, requestContext), ctx.Err()
			}
			result.Status = ResultStatusDriverError
			result.Error = err.Error()
			result.Steps = turnSteps
			return a.finishResult(result, requestContext), nil
		}
		requestContext = requestContext.withProviderState(decision.ProviderState)
		result.Usage = mergeUsage(result.Usage, decision.Usage)
		if err := validateDecision(decision); err != nil {
			result.Status = ResultStatusDriverError
			result.Error = err.Error()
			result.Steps = turnSteps
			return a.finishResult(result, requestContext), nil
		}
		if decision.Finish != nil {
			result.Status = ResultStatusFinished
			result.FinishThought = strings.TrimSpace(decision.Thought)
			result.Value = decision.Finish.Value
			result.Steps = turnSteps
			return a.finishResult(result, requestContext), nil
		}

		stepIndex := nextStepIndex + len(turnSteps)
		step, runErr := a.runToolStep(ctx, input.Trigger, stepIndex, decision)
		turnSteps = append(turnSteps, step)
		requestContext = requestContext.withStepReplayData(StepCallID(step), decision.ReplayData)
		if a.onStep != nil {
			if stepErr := a.onStep.HandleStep(ctx, input.Trigger, step); stepErr != nil {
				return a.finishResult(Result{
					Trigger:   input.Trigger,
					StartedAt: result.StartedAt,
					Status:    ResultStatusFatalError,
					CWD:       a.runner.Dir,
					Usage:     result.Usage,
					Steps:     turnSteps,
					Error:     stepErr.Error(),
				}, requestContext), stepErr
			}
		}
		if runErr != nil {
			if ctx.Err() != nil && (errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded)) {
				result.Status = ResultStatusCanceled
				result.Error = ctx.Err().Error()
				result.Steps = turnSteps
				return a.finishResult(result, requestContext), ctx.Err()
			}
			result.Status = ResultStatusFatalError
			result.Error = runErr.Error()
			result.Steps = turnSteps
			return a.finishResult(result, requestContext), nil
		}
	}

	result.Status = ResultStatusMaxSteps
	result.Error = fmt.Sprintf("agent reached max steps (%d)", a.maxSteps)
	result.Steps = turnSteps
	return a.finishResult(result, requestContext), nil
}

func (a *Agent) stepContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if a.stepTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, a.stepTimeout)
}

func shouldRetryDriverError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err)
}

func (a *Agent) nextDecision(ctx context.Context, request Request) (Decision, error) {
	for retry := 0; ; retry++ {
		attemptCtx, cancel := a.stepContext(ctx)
		decision, err := a.driver.Next(attemptCtx, request)
		cancel()
		if err == nil {
			return decision, nil
		}
		if ctx.Err() != nil {
			return Decision{}, err
		}
		if retry >= maxDriverRetries || !shouldRetryDriverError(err) {
			return Decision{}, err
		}
		a.logDriverRetry(request.Step, retry+1, err)
	}
}

func (a *Agent) logDriverRetry(step, retry int, err error) {
	if a.liveStderr == nil {
		return
	}
	_, _ = fmt.Fprintf(
		a.liveStderr,
		"driver step %d error (retry %d/%d): %v\n",
		step,
		retry,
		maxDriverRetries,
		err,
	)
}

func (a *Agent) handleResult(ctx context.Context, result Result) error {
	if a.onResult == nil {
		return nil
	}
	return a.onResult.HandleResult(ctx, result)
}

func classifyRunResult(step Step, err error) (Step, error) {
	if err == nil {
		step.Status = StepStatusOK
		return step, nil
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		step.Status = StepStatusFatalError
		step.Error = err.Error()
		return step, err
	}

	var exitStatus interp.ExitStatus
	if errors.As(err, &exitStatus) {
		step.Status = StepStatusExitStatus
		step.ExitStatus = int(exitStatus)
		step.Error = err.Error()
		return step, nil
	}

	var coreutilsErr *coreutils.Error
	if errors.As(err, &coreutilsErr) {
		step.Status = StepStatusExitStatus
		step.ExitStatus = 1
		step.Error = err.Error()
		return step, nil
	}

	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		step.Status = StepStatusExitStatus
		step.ExitStatus = 1
		step.Error = err.Error()
		return step, nil
	}

	step.Status = StepStatusFatalError
	step.Error = err.Error()
	return step, err
}

func validateDecision(decision Decision) error {
	hasTool := decision.Tool != nil
	hasFinish := decision.Finish != nil
	count := 0
	if hasTool {
		count++
	}
	if hasFinish {
		count++
	}
	switch {
	case count != 1:
		return fmt.Errorf("decision must set exactly one of Tool or Finish")
	case hasTool && strings.TrimSpace(decision.Tool.Name) == "":
		return fmt.Errorf("decision tool name must not be empty")
	case hasTool && strings.TrimSpace(decision.Tool.Input) == "" && decision.Tool.Kind == ToolKindCustom:
		return fmt.Errorf("decision custom tool input must not be empty")
	default:
		return nil
	}
}

func blockControlBuiltins(ctx context.Context, args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("agent call handler received empty args")
	}
	switch args[0] {
	case "exec", "exit":
		hc := interp.HandlerCtx(ctx)
		fmt.Fprintf(hc.Stderr, "%s: disabled in agent shell steps\n", args[0])
		return []string{"false"}, nil
	default:
		return args, nil
	}
}

func (a *Agent) finishResult(result Result, requestContext driverRequestContext) Result {
	result.FinishedAt = time.Now()
	result.Duration = result.FinishedAt.Sub(result.StartedAt)
	result.CWD = a.runner.Dir
	result.Steps = cloneSteps(result.Steps)
	result.StepReplayData = cloneStepReplayData(requestContext.StepReplayData)
	result.ProviderState = strings.TrimSpace(requestContext.ProviderState)
	return result
}

func finishStep(step *Step) {
	step.FinishedAt = time.Now()
	step.Duration = step.FinishedAt.Sub(step.StartedAt)
}

func cloneSteps(steps []Step) []Step {
	if len(steps) == 0 {
		return nil
	}
	cloned := make([]Step, 0, len(steps))
	for _, step := range steps {
		current := step
		if len(step.ActionOutputFiles) > 0 {
			current.ActionOutputFiles = slices.Clone(step.ActionOutputFiles)
		}
		if step.StdoutFile != nil {
			file := *step.StdoutFile
			current.StdoutFile = &file
		}
		if step.StderrFile != nil {
			file := *step.StderrFile
			current.StderrFile = &file
		}
		cloned = append(cloned, current)
	}
	return cloned
}

func mergeUsage(dst, src Usage) Usage {
	dst.CachedTokens += src.CachedTokens
	return dst
}

func (a *Agent) toolHTTPClient(toolName string, opts ToolHTTPClientOptions) *http.Client {
	if a == nil {
		return nil
	}
	factory := a.toolHTTPClientFactories[toolName]
	if factory == nil {
		return nil
	}
	return factory.NewClient(interp.HTTPClientOptions{
		ConnectTimeout:  opts.ConnectTimeout,
		FollowRedirects: opts.FollowRedirects,
	})
}

func newAgentScopedNetworkDialer(base interp.NetworkDialer, reachableHosts []interp.HostMatcher) (interp.NetworkDialer, error) {
	if len(reachableHosts) == 0 || base == nil {
		return nil, nil
	}
	return interp.NewAllowlistNetworkDialer(base, reachableHosts)
}

func newAgentHTTPClientFactory(
	base interp.NetworkDialer,
	reachableHosts []interp.HostMatcher,
	headers []interp.HTTPHeaderRule,
) (interp.HTTPClientFactory, error) {
	dialer, err := newAgentScopedNetworkDialer(base, reachableHosts)
	if err != nil {
		return nil, err
	}
	return interp.NewHTTPClientFactory(dialer, headers)
}

func effectiveToolReachableHosts(
	scope ToolNetworkScope,
	globalReachableHosts []interp.HostMatcher,
	requiredHosts []interp.HostMatcher,
) []interp.HostMatcher {
	switch scope.Normalized() {
	case ToolNetworkScopeNone:
		return nil
	case ToolNetworkScopeGlobal:
		return slices.Clone(globalReachableHosts)
	case ToolNetworkScopeScoped:
		return mergeHostMatchers(globalReachableHosts, requiredHosts)
	default:
		return nil
	}
}

func mergeHostMatchers(left, right []interp.HostMatcher) []interp.HostMatcher {
	if len(left) == 0 && len(right) == 0 {
		return nil
	}
	seen := make(map[interp.HostMatcher]struct{}, len(left)+len(right))
	merged := make([]interp.HostMatcher, 0, len(left)+len(right))
	appendMatchers := func(values []interp.HostMatcher) {
		for _, value := range values {
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			merged = append(merged, value)
		}
	}
	appendMatchers(left)
	appendMatchers(right)
	return merged
}
