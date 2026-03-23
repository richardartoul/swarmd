// See LICENSE for licensing information

package agent

import (
	"context"
	"io"
	"time"

	"github.com/richardartoul/swarmd/pkg/sh/interp"
	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
)

type WebSearchBackend = toolscore.WebSearchBackend
type WebSearchResponse = toolscore.WebSearchResponse
type WebSearchResult = toolscore.WebSearchResult
type ImageDescriptionBackend = toolscore.ImageDescriptionBackend
type ImageDescriptionRequest = toolscore.ImageDescriptionRequest
type ImageDescriptionResponse = toolscore.ImageDescriptionResponse

// Queue supplies triggers to a long-running agent.
type Queue interface {
	// Next blocks until the next trigger is available or ctx is canceled.
	Next(ctx context.Context) (Trigger, error)
}

// QueueFunc adapts a function to [Queue].
type QueueFunc func(ctx context.Context) (Trigger, error)

// Next implements [Queue].
func (f QueueFunc) Next(ctx context.Context) (Trigger, error) {
	return f(ctx)
}

// Driver decides what the agent should do next for a trigger.
type Driver interface {
	Next(ctx context.Context, req Request) (Decision, error)
}

// DriverFunc adapts a function to [Driver].
type DriverFunc func(ctx context.Context, req Request) (Decision, error)

// Next implements [Driver].
func (f DriverFunc) Next(ctx context.Context, req Request) (Decision, error) {
	return f(ctx, req)
}

// ResultHandler receives completed per-trigger results from [Agent.Serve].
type ResultHandler interface {
	HandleResult(ctx context.Context, result Result) error
}

// StepHandler receives completed per-step results from [Agent.HandleTrigger].
type StepHandler interface {
	HandleStep(ctx context.Context, trigger Trigger, step Step) error
}

// ResultHandlerFunc adapts a function to [ResultHandler].
type ResultHandlerFunc func(ctx context.Context, result Result) error

// HandleResult implements [ResultHandler].
func (f ResultHandlerFunc) HandleResult(ctx context.Context, result Result) error {
	return f(ctx, result)
}

// StepHandlerFunc adapts a function to [StepHandler].
type StepHandlerFunc func(ctx context.Context, trigger Trigger, step Step) error

// HandleStep implements [StepHandler].
func (f StepHandlerFunc) HandleStep(ctx context.Context, trigger Trigger, step Step) error {
	return f(ctx, trigger, step)
}

// Config configures an [Agent].
type Config struct {
	// FileSystem is the sandbox filesystem backend the agent shell should run
	// against, such as the default disk-backed sandbox or an in-memory backend.
	FileSystem sandbox.FileSystem

	// Root constructs a default disk-backed sandbox filesystem when FileSystem is nil.
	Root string

	// NetworkDialer is the raw outbound dialer used to construct host-scoped
	// clients for shell commands and structured tools. If nil, any tool or shell
	// path that needs outbound networking will reject dialing.
	NetworkDialer interp.NetworkDialer

	// GlobalReachableHosts controls outbound networking for shell commands and
	// generic URL tools such as curl, http_request, read_web_page, and web_search.
	// Structured tools with fixed endpoints may carry their own scoped hosts in
	// registry metadata and do not need to be repeated here.
	GlobalReachableHosts []interp.HostMatcher

	// HTTPHeaders are automatically applied to outbound interpreter-owned HTTP
	// requests such as curl when their domain matchers apply.
	HTTPHeaders []interp.HTTPHeaderRule

	// CustomCommands are additional host-provided sandbox commands exposed to
	// run_shell steps.
	CustomCommands []sandbox.CustomCommand

	// ConfiguredTools are explicit custom tool instances enabled for this agent.
	// Built-in structured tools are always available subject to capability gates
	// such as network access.
	ConfiguredTools []ConfiguredTool

	// ToolRuntimeData carries host-owned, per-agent runtime data that custom
	// structured tools may type-assert via [ToolContext.RuntimeData].
	ToolRuntimeData any

	// WebSearchBackend overrides the runtime-owned backend used by the
	// web_search tool. If nil, the default DuckDuckGo HTML backend is used.
	WebSearchBackend WebSearchBackend

	// ImageDescriptionBackend overrides the runtime-owned backend used by the
	// describe_image tool. If nil and the configured driver also implements
	// [ImageDescriptionBackend], the driver is reused automatically.
	ImageDescriptionBackend ImageDescriptionBackend

	// Queue is optional unless [Agent.Serve] is used.
	Queue Queue

	// Driver decides the next action for each step.
	Driver Driver

	// SystemPrompt is the system instruction prepended to driver requests.
	// If empty, [DefaultSystemPrompt] is used.
	SystemPrompt string

	// OnResult is called after [Agent.Serve] handles one trigger.
	OnResult ResultHandler

	// OnStep is called after each action step completes.
	OnStep StepHandler

	// MaxSteps bounds the number of driver decisions per trigger.
	// If zero, [DefaultMaxSteps] is used.
	MaxSteps int

	// StepTimeout bounds one driver attempt or action step. Retries receive a
	// fresh timeout budget. Zero means no timeout.
	StepTimeout time.Duration

	// MaxOutputBytes bounds the captured bytes per stream per step.
	// If zero, [DefaultMaxOutputBytes] is used.
	MaxOutputBytes int

	// OutputFileThresholdBytes controls when the runtime should spill tool output
	// or shell streams into per-run sandbox files while keeping only bounded
	// previews inline. If zero, the runtime uses the current preview budget.
	OutputFileThresholdBytes int

	// PreserveStateBetweenTriggers keeps shell state warm across triggers.
	// The zero value resets state before each new trigger.
	PreserveStateBetweenTriggers bool

	// Stdout and Stderr mirror live step output while still being captured.
	Stdout io.Writer
	Stderr io.Writer
}

// Trigger is one unit of work consumed from a [Queue].
type Trigger struct {
	ID         string
	Kind       string
	Payload    any
	Metadata   map[string]any
	EnqueuedAt time.Time
}

const (
	// MessageRoleSystem identifies a system message.
	MessageRoleSystem = "system"

	// MessageRoleUser identifies a user message.
	MessageRoleUser = "user"

	// MessageRoleAssistant identifies an assistant message.
	MessageRoleAssistant = "assistant"
)

// Message is one provider-neutral prompt message passed to a driver.
type Message struct {
	Role    string
	Content string
}

type ToolKind = toolscore.ToolKind

const (
	ToolKindFunction = toolscore.ToolKindFunction
	ToolKindCustom   = toolscore.ToolKindCustom
)

type ToolFormat = toolscore.ToolFormat
type ToolBoundaryKind = toolscore.ToolBoundaryKind

const (
	ToolBoundaryKindFunction   = toolscore.ToolBoundaryKindFunction
	ToolBoundaryKindCustom     = toolscore.ToolBoundaryKindCustom
	ToolBoundaryKindLocalShell = toolscore.ToolBoundaryKindLocalShell
	ToolBoundaryKindWebSearch  = toolscore.ToolBoundaryKindWebSearch
	ToolBoundaryKindToolSearch = toolscore.ToolBoundaryKindToolSearch
)

type ToolNetworkScope = toolscore.ToolNetworkScope

const (
	ToolNetworkScopeNone   = toolscore.ToolNetworkScopeNone
	ToolNetworkScopeGlobal = toolscore.ToolNetworkScopeGlobal
	ToolNetworkScopeScoped = toolscore.ToolNetworkScopeScoped
)

type ToolInterop = toolscore.ToolInterop
type ToolDefinition = toolscore.ToolDefinition
type FileReference = toolscore.FileReference

// Request is the input to one [Driver.Next] call.
// Messages holds the fully prepared prompt context. The remaining fields expose
// the originating agent state for wrappers and custom drivers.
type Request struct {
	Trigger     Trigger
	Step        int
	SandboxRoot string
	CWD         string
	// Steps contains all prior replayable steps visible to this request across
	// both prior session turns and the current in-flight turn.
	Steps []Step
	// ConversationTurns captures prior session turns in chronological order. It
	// is empty for one-shot trigger handling.
	ConversationTurns []ConversationTurn
	// CurrentTurnMessages are the current turn's provider-neutral prompt
	// messages in order: current user turn, protocol reminder, then current
	// execution-state footer.
	CurrentTurnMessages []Message
	// CurrentTurnSteps contains only the already executed steps from the current
	// in-flight turn. Unlike [Steps], it never includes steps from prior turns.
	CurrentTurnSteps []Step
	Tools            []ToolDefinition
	Messages         []Message
	// StepReplayData carries opaque provider-specific replay metadata keyed by
	// [StepCallID]. Drivers may use it to reconstruct native multi-step context.
	StepReplayData map[string]string
	// ProviderState carries opaque provider-specific turn state. Drivers may use
	// it to continue native conversations or reconstruct provider-native context
	// across requests and session turns.
	ProviderState string
}

type Usage = toolscore.Usage

// Decision is one driver response.
type Decision struct {
	Thought string
	Usage   Usage
	Tool    *ToolAction
	Finish  *FinishAction
	// ReplayData carries opaque provider-specific metadata for the action
	// represented by this decision so later requests can replay native context.
	ReplayData string
	// ProviderState carries opaque provider-specific turn state produced by this
	// decision so later requests can continue native provider context.
	ProviderState string
}

type ToolAction = toolscore.ToolAction

// FinishAction ends the current trigger run with a final value.
type FinishAction struct {
	Value any
}

type StepStatus = toolscore.StepStatus

const (
	StepStatusOK          = toolscore.StepStatusOK
	StepStatusExitStatus  = toolscore.StepStatusExitStatus
	StepStatusParseError  = toolscore.StepStatusParseError
	StepStatusPolicyError = toolscore.StepStatusPolicyError
	StepStatusFatalError  = toolscore.StepStatusFatalError
)

// ResultStatus classifies one trigger run outcome.
type ResultStatus string

const (
	ResultStatusFinished    ResultStatus = "finished"
	ResultStatusMaxSteps    ResultStatus = "max_steps"
	ResultStatusDriverError ResultStatus = "driver_error"
	ResultStatusFatalError  ResultStatus = "fatal_error"
	ResultStatusCanceled    ResultStatus = "canceled"
)

type StepType = toolscore.StepType

const (
	StepTypeShell = toolscore.StepTypeShell
	StepTypeTool  = toolscore.StepTypeTool
)

type Step = toolscore.Step

// Result records handling of one trigger.
type Result struct {
	Trigger       Trigger
	StartedAt     time.Time
	FinishedAt    time.Time
	Duration      time.Duration
	Status        ResultStatus
	CWD           string
	Usage         Usage
	FinishThought string
	Value         any
	Steps         []Step
	Error         string
	// StepReplayData carries the opaque per-step replay metadata accumulated over
	// this result's conversation state so sessions can preserve native step
	// context across turns.
	StepReplayData map[string]string
	// ProviderState carries opaque provider-specific turn state so sessions can
	// preserve native provider context across turns.
	ProviderState string
}

const (
	// DefaultMaxSteps is used when [Config.MaxSteps] is zero.
	DefaultMaxSteps = 32

	// DefaultMaxOutputBytes is used when [Config.MaxOutputBytes] is zero.
	DefaultMaxOutputBytes = 64 << 10
)
