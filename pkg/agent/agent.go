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
	queue                Queue
	driver               Driver
	onResult             ResultHandler
	onStep               StepHandler
	parser               *syntax.Parser
	runner               *interp.Runner
	fileSystem           sandbox.FileSystem
	sandboxRoot          string
	httpClientFactory    interp.HTTPClientFactory
	stdout               *switchWriter
	stderr               *switchWriter
	maxSteps             int
	maxOutputBytes       int
	stepTimeout          time.Duration
	preserveState        bool
	systemPrompt         string
	liveStdout           io.Writer
	liveStderr           io.Writer
	networkEnabled       bool
	toolDefinitions      []ToolDefinition
	toolByName           map[string]ToolDefinition
	toolHandlerByName    map[string]ToolHandler
	toolRuntimeData      any
	webSearchBackend     WebSearchBackend
	customCommands       []sandbox.CommandInfo
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
		queue:                cfg.Queue,
		driver:               cfg.Driver,
		onResult:             cfg.OnResult,
		onStep:               cfg.OnStep,
		parser:               syntax.NewParser(syntax.Variant(syntax.LangPOSIX)),
		runner:               runner,
		fileSystem:           policy,
		sandboxRoot:          sandboxRoot,
		httpClientFactory:    httpClientFactory,
		stdout:               stdout,
		stderr:               stderr,
		maxSteps:             maxSteps,
		maxOutputBytes:       maxOutputBytes,
		stepTimeout:          cfg.StepTimeout,
		preserveState:        cfg.PreserveStateBetweenTriggers,
		systemPrompt:         systemPrompt,
		liveStdout:           cfg.Stdout,
		liveStderr:           cfg.Stderr,
		networkEnabled:       networkEnabled,
		toolDefinitions:      toolDefinitions,
		toolByName:           toolByName,
		toolHandlerByName:    toolHandlerByName,
		toolRuntimeData:      cfg.ToolRuntimeData,
		webSearchBackend:     webSearchBackend,
		customCommands:       commandInfosFromCustomCommands(cfg.CustomCommands),
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
		if a.onResult != nil {
			if err := a.onResult.HandleResult(ctx, result); err != nil {
				return err
			}
		}
	}
}

// HandleTrigger handles a single trigger synchronously.
func (a *Agent) HandleTrigger(ctx context.Context, trigger Trigger) (Result, error) {
	result := Result{
		Trigger:   trigger,
		StartedAt: time.Now(),
	}

	if !a.preserveState {
		a.runner.Reset()
	}

	steps := make([]Step, 0, a.maxSteps)
	for stepNum := 1; stepNum <= a.maxSteps; stepNum++ {
		if err := ctx.Err(); err != nil {
			result.Status = ResultStatusCanceled
			result.Error = err.Error()
			result.Steps = cloneSteps(steps)
			return a.finishResult(result), err
		}

		stepsSnapshot := cloneSteps(steps)
		request, _, err := a.buildDriverRequest(trigger, stepNum, a.runner.Dir, stepsSnapshot)
		if err != nil {
			result.Status = ResultStatusDriverError
			result.Error = err.Error()
			result.Steps = stepsSnapshot
			return a.finishResult(result), nil
		}

		decision, err := a.driver.Next(ctx, request)
		if err != nil {
			result.Status = ResultStatusDriverError
			result.Error = err.Error()
			result.Steps = stepsSnapshot
			return a.finishResult(result), nil
		}
		result.Usage = mergeUsage(result.Usage, decision.Usage)
		if err := validateDecision(decision); err != nil {
			result.Status = ResultStatusDriverError
			result.Error = err.Error()
			result.Steps = stepsSnapshot
			return a.finishResult(result), nil
		}
		if decision.Finish != nil {
			result.Status = ResultStatusFinished
			result.FinishThought = strings.TrimSpace(decision.Thought)
			result.Value = decision.Finish.Value
			result.Steps = stepsSnapshot
			return a.finishResult(result), nil
		}

		var (
			step   Step
			runErr error
		)
		switch {
		case decision.Tool != nil:
			step, runErr = a.runToolStep(ctx, trigger, stepNum, decision)
		default:
			step, runErr = a.runShellStep(ctx, trigger, stepNum, decision)
		}
		steps = append(steps, step)
		if a.onStep != nil {
			if stepErr := a.onStep.HandleStep(ctx, trigger, step); stepErr != nil {
				return a.finishResult(Result{
					Trigger:   trigger,
					StartedAt: result.StartedAt,
					Status:    ResultStatusFatalError,
					CWD:       a.runner.Dir,
					Usage:     result.Usage,
					Steps:     cloneSteps(steps),
					Error:     stepErr.Error(),
				}), stepErr
			}
		}
		if runErr != nil {
			if ctx.Err() != nil && (errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded)) {
				result.Status = ResultStatusCanceled
				result.Error = ctx.Err().Error()
				result.Steps = cloneSteps(steps)
				return a.finishResult(result), ctx.Err()
			}
			result.Status = ResultStatusFatalError
			result.Error = runErr.Error()
			result.Steps = cloneSteps(steps)
			return a.finishResult(result), nil
		}
	}

	result.Status = ResultStatusMaxSteps
	result.Error = fmt.Sprintf("agent reached max steps (%d)", a.maxSteps)
	result.Steps = cloneSteps(steps)
	return a.finishResult(result), nil
}

func (a *Agent) runShellStep(ctx context.Context, trigger Trigger, stepNum int, decision Decision) (Step, error) {
	step := Step{
		Index:      stepNum,
		Type:       StepTypeShell,
		Thought:    decision.Thought,
		ActionName: ToolNameRunShell,
		Shell:      decision.Shell.Source,
		Usage:      decision.Usage,
		CWDBefore:  a.runner.Dir,
		CWDAfter:   a.runner.Dir,
		StartedAt:  time.Now(),
	}
	defer finishStep(&step)

	prog, err := a.parser.Parse(strings.NewReader(decision.Shell.Source), fmt.Sprintf("agent-step-%d", stepNum))
	if err != nil {
		step.Status = StepStatusParseError
		step.Error = err.Error()
		return step, nil
	}
	if err := validateProgram(prog); err != nil {
		step.Status = StepStatusPolicyError
		step.Error = err.Error()
		return step, nil
	}

	stdoutCapture := newCaptureWriter(a.maxOutputBytes, a.liveStdout)
	stderrCapture := newCaptureWriter(a.maxOutputBytes, a.liveStderr)
	a.stdout.Set(stdoutCapture)
	a.stderr.Set(stderrCapture)
	defer func() {
		a.stdout.Set(io.Discard)
		a.stderr.Set(io.Discard)
	}()

	runCtx := ctx
	cancel := func() {}
	if a.stepTimeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, a.stepTimeout)
	}
	defer cancel()
	runCtx = contextWithTrigger(runCtx, trigger)

	err = a.runner.Run(runCtx, prog)
	step.CWDAfter = a.runner.Dir
	step.Stdout, step.StdoutTruncated = stdoutCapture.Snapshot()
	step.Stderr, step.StderrTruncated = stderrCapture.Snapshot()
	return classifyRunResult(step, err)
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
	hasShell := decision.Shell != nil
	hasTool := decision.Tool != nil
	hasFinish := decision.Finish != nil
	count := 0
	if hasShell {
		count++
	}
	if hasTool {
		count++
	}
	if hasFinish {
		count++
	}
	switch {
	case count != 1:
		return fmt.Errorf("decision must set exactly one of Shell, Tool, or Finish")
	case hasShell && strings.TrimSpace(decision.Shell.Source) == "":
		return fmt.Errorf("decision shell source must not be empty")
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

func (a *Agent) finishResult(result Result) Result {
	result.FinishedAt = time.Now()
	result.Duration = result.FinishedAt.Sub(result.StartedAt)
	result.CWD = a.runner.Dir
	result.Steps = cloneSteps(result.Steps)
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
