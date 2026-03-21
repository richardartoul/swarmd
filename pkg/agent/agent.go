// See LICENSE for licensing information

package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/richardartoul/swarmd/pkg/sh/interp"
	"github.com/richardartoul/swarmd/pkg/sh/moreinterp/coreutils"
	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
	"github.com/richardartoul/swarmd/pkg/sh/syntax"
)

// ErrQueueRequired is returned when [Agent.Serve] is called without a queue.
var ErrQueueRequired = errors.New("agent queue is required for Serve")

// Agent runs a queue-driven agent loop on top of a sandboxed shell.
type Agent struct {
	queue             Queue
	driver            Driver
	onResult          ResultHandler
	onStep            StepHandler
	parser            *syntax.Parser
	runner            *interp.Runner
	fileSystem        sandbox.FileSystem
	sandboxRoot       string
	httpClientFactory interp.HTTPClientFactory
	stdout            *switchWriter
	stderr            *switchWriter
	maxSteps          int
	maxOutputBytes    int
	stepTimeout       time.Duration
	preserveState     bool
	systemPrompt      string
	liveStdout        io.Writer
	liveStderr        io.Writer
	networkEnabled    bool
	toolDefinitions   []ToolDefinition
	toolByName        map[string]ToolDefinition
	toolHandlerByName map[string]ToolHandler
	toolRuntimeData   any
	webSearchBackend  WebSearchBackend
	customCommands    []sandbox.CommandInfo
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
	networkEnabled := cfg.NetworkEnabled || cfg.NetworkDialer != nil
	httpClientFactory, err := interp.NewHTTPClientFactory(cfg.NetworkDialer, cfg.HTTPHeaders)
	if err != nil {
		return nil, fmt.Errorf("create agent HTTP client factory: %w", err)
	}
	toolBindings, err := resolveToolBindings(cfg.ConfiguredTools, networkEnabled)
	if err != nil {
		return nil, fmt.Errorf("resolve agent tools: %w", err)
	}
	toolDefinitions := make([]ToolDefinition, 0, len(toolBindings))
	toolByName := make(map[string]ToolDefinition, len(toolBindings))
	toolHandlerByName := make(map[string]ToolHandler, len(toolBindings))
	for _, binding := range toolBindings {
		toolDefinitions = append(toolDefinitions, binding.Definition)
		toolByName[binding.Definition.Name] = binding.Definition
		toolHandlerByName[binding.Definition.Name] = binding.Handler
	}
	webSearchBackend := cfg.WebSearchBackend
	if webSearchBackend == nil {
		webSearchBackend = NewDuckDuckGoWebSearchBackend()
	}
	runner, err := sandbox.NewRunnerWithConfig(policy, sandbox.RunnerConfig{
		Stdout:            stdout,
		Stderr:            stderr,
		NetworkDialer:     cfg.NetworkDialer,
		NetworkEnabled:    networkEnabled,
		HTTPHeaders:       cfg.HTTPHeaders,
		HTTPClientFactory: httpClientFactory,
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
	systemPrompt := strings.TrimSpace(cfg.SystemPrompt)
	if systemPrompt == "" {
		systemPrompt = defaultSystemPrompt(networkEnabled)
	}

	return &Agent{
		queue:             cfg.Queue,
		driver:            cfg.Driver,
		onResult:          cfg.OnResult,
		onStep:            cfg.OnStep,
		parser:            syntax.NewParser(syntax.Variant(syntax.LangPOSIX)),
		runner:            runner,
		fileSystem:        policy,
		sandboxRoot:       sandboxRoot,
		httpClientFactory: httpClientFactory,
		stdout:            stdout,
		stderr:            stderr,
		maxSteps:          maxSteps,
		maxOutputBytes:    maxOutputBytes,
		stepTimeout:       cfg.StepTimeout,
		preserveState:     cfg.PreserveStateBetweenTriggers,
		systemPrompt:      systemPrompt,
		liveStdout:        cfg.Stdout,
		liveStderr:        cfg.Stderr,
		networkEnabled:    networkEnabled,
		toolDefinitions:   toolDefinitions,
		toolByName:        toolByName,
		toolHandlerByName: toolHandlerByName,
		toolRuntimeData:   cfg.ToolRuntimeData,
		webSearchBackend:  webSearchBackend,
		customCommands:    commandInfosFromCustomCommands(cfg.CustomCommands),
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

		decision, err := a.driver.Next(ctx, request)
		if err != nil {
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
	return slices.Clone(steps)
}

func mergeUsage(dst, src Usage) Usage {
	dst.CachedTokens += src.CachedTokens
	return dst
}
