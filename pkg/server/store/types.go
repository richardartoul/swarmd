package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
)

// AgentRole classifies an agent record; only workers are runnable today.
type AgentRole string

const (
	AgentRolePrimary AgentRole = "primary"
	AgentRoleWorker  AgentRole = "worker"
)

// AgentDesiredState is the operator-requested lifecycle state of an agent.
type AgentDesiredState string

const (
	AgentDesiredStateRunning AgentDesiredState = "running"
	AgentDesiredStatePaused  AgentDesiredState = "paused"
	AgentDesiredStateStopped AgentDesiredState = "stopped"
)

// MailboxMessageStatus is the delivery state of a mailbox message.
type MailboxMessageStatus string

const (
	MailboxMessageStatusQueued     MailboxMessageStatus = "queued"
	MailboxMessageStatusLeased     MailboxMessageStatus = "leased"
	MailboxMessageStatusCompleted  MailboxMessageStatus = "completed"
	MailboxMessageStatusDeadLetter MailboxMessageStatus = "dead_letter"
)

// RunStatus is the lifecycle state of a run record.
type RunStatus string

const (
	RunStatusRunning RunStatus = "running"
)

const DefaultAgentStepTimeout = 5 * time.Minute

// JSONEnvelope wraps stored JSON with the kind tag used for validation.
type JSONEnvelope struct {
	Version int             `json:"version"`
	Type    string          `json:"type,omitempty"`
	Body    json.RawMessage `json:"body"`
}

// Namespace is one tenant boundary; agents and mailboxes are scoped to it.
type Namespace struct {
	ID         string
	Name       string
	LimitsJSON string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// AgentRecord is the stored configuration of one agent.
type AgentRecord struct {
	NamespaceID            string
	ID                     string
	Name                   string
	Role                   AgentRole
	DesiredState           AgentDesiredState
	RootPath               string
	ModelProvider          string
	ModelName              string
	ModelBaseURL           string
	PreserveState          bool
	MaxSteps               int
	StepTimeout            time.Duration
	MaxOutputBytes         int
	LeaseDuration          time.Duration
	RetryDelay             time.Duration
	MaxAttempts            int
	ConfigJSON             string
	CurrentPromptVersionID string
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// RunnableAgent joins an agent record with its current system prompt.
type RunnableAgent struct {
	AgentRecord
	SystemPrompt string
}

// AgentPromptVersion is one immutable revision of an agent's prompt and
// action schema.
type AgentPromptVersion struct {
	NamespaceID      string
	ID               string
	AgentID          string
	Version          int
	Prompt           string
	ActionSchemaJSON string
	CreatedAt        time.Time
}

// MailboxMessageRecord is one stored mailbox message with its lease state.
type MailboxMessageRecord struct {
	NamespaceID      string
	ID               string
	ThreadID         string
	SenderAgentID    string
	RecipientAgentID string
	Kind             string
	PayloadJSON      string
	MetadataJSON     string
	Status           MailboxMessageStatus
	AvailableAt      time.Time
	LeaseOwner       string
	LeaseExpiresAt   *time.Time
	AttemptCount     int
	MaxAttempts      int
	RunID            string
	DeadLetterReason string
	LastError        string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	ClaimedAt        *time.Time
	CompletedAt      *time.Time
}

// ScheduleRecord is one stored cron schedule with its fire times.
type ScheduleRecord struct {
	NamespaceID string
	ID          string
	AgentID     string
	CronExpr    string
	TimeZone    string
	PayloadJSON string
	Enabled     bool
	NextFireAt  *time.Time
	LastFireAt  *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// RunRecord is one stored agent run with status, usage, and result.
type RunRecord struct {
	NamespaceID   string
	ID            string
	MessageID     string
	AgentID       string
	TriggerID     string
	Status        string
	StartedAt     time.Time
	FinishedAt    *time.Time
	Duration      time.Duration
	CWD           string
	Usage         toolscore.Usage
	FinishThought string
	ValueJSON     string
	Error         string
	TriggerPrompt string
	SystemPrompt  string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// StepRecord is one stored step within a run.
type StepRecord struct {
	NamespaceID           string
	RunID                 string
	MessageID             string
	AgentID               string
	StepIndex             int
	StepType              string
	Thought               string
	Shell                 string
	ActionName            string
	ActionToolKind        string
	ActionInput           string
	ActionOutput          string
	ActionOutputTruncated bool
	Usage                 toolscore.Usage
	CWDBefore             string
	CWDAfter              string
	Stdout                string
	Stderr                string
	StdoutTruncated       bool
	StderrTruncated       bool
	StartedAt             time.Time
	FinishedAt            time.Time
	Duration              time.Duration
	Status                string
	ExitStatus            int
	Error                 string
}

// MailboxThreadMessage is the thread-view projection of a mailbox message.
type MailboxThreadMessage struct {
	ID               string
	ThreadID         string
	SenderAgentID    string
	RecipientAgentID string
	Kind             string
	PayloadJSON      string
	Status           MailboxMessageStatus
	CreatedAt        time.Time
	CompletedAt      *time.Time
}

// NamespaceSnapshot aggregates a namespace's agents, schedules, and mailbox
// counters for inspection surfaces.
type NamespaceSnapshot struct {
	Namespace Namespace
	Agents    []RunnableAgent
	Schedules []ScheduleRecord
	Mailbox   MailboxSummary
}

// MailboxSummary counts mailbox messages by status.
type MailboxSummary struct {
	Queued     int
	Leased     int
	DeadLetter int
	Completed  int
}

// CreateNamespaceParams configures [Store.CreateNamespace].
type CreateNamespaceParams struct {
	ID     string
	Name   string
	Limits any
}

// PutNamespaceResult reports what [Store.PutNamespace] changed.
type PutNamespaceResult struct {
	Namespace Namespace
	Created   bool
	Updated   bool
}

// ListAgentsParams filters [Store.ListAgents].
type ListAgentsParams struct {
	NamespaceID string
}

// CreateAgentParams configures [Store.CreateAgent]; zero fields receive
// server defaults.
type CreateAgentParams struct {
	NamespaceID    string
	AgentID        string
	Name           string
	Role           AgentRole
	DesiredState   AgentDesiredState
	RootPath       string
	ModelProvider  string
	ModelName      string
	ModelBaseURL   string
	PreserveState  bool
	MaxSteps       int
	StepTimeout    time.Duration
	MaxOutputBytes int
	LeaseDuration  time.Duration
	RetryDelay     time.Duration
	MaxAttempts    int
	Config         any
	SystemPrompt   string
	ActionSchema   any
}

// UpdateAgentPromptParams configures [Store.UpdateAgentPrompt].
type UpdateAgentPromptParams struct {
	NamespaceID  string
	AgentID      string
	Prompt       string
	ActionSchema any
}

// UpdateAgentDesiredStateParams configures [Store.UpdateAgentDesiredState].
type UpdateAgentDesiredStateParams struct {
	NamespaceID  string
	AgentID      string
	DesiredState AgentDesiredState
}

// CreateMailboxMessageParams configures [Store.EnqueueMessage].
type CreateMailboxMessageParams struct {
	NamespaceID      string
	MessageID        string
	ThreadID         string
	SenderAgentID    string
	RecipientAgentID string
	Kind             string
	Payload          any
	Metadata         any
	AvailableAt      time.Time
	MaxAttempts      int
}

// ClaimMessageParams configures [Store.ClaimNextMessage].
type ClaimMessageParams struct {
	NamespaceID   string
	AgentID       string
	LeaseOwner    string
	LeaseDuration time.Duration
	SystemPrompt  string
}

// ClaimedMailboxMessage is one leased message paired with its new run.
type ClaimedMailboxMessage struct {
	Message MailboxMessageRecord
	Run     RunRecord
}

// CompleteRunParams configures [Store.CompleteRun]. RetryAt requeues the
// message; DeadLetterReason retires it; otherwise it completes.
type CompleteRunParams struct {
	NamespaceID      string
	RunID            string
	MessageID        string
	Status           string
	FinishedAt       time.Time
	Duration         time.Duration
	CWD              string
	Usage            toolscore.Usage
	FinishThought    string
	Value            any
	Error            string
	RetryAt          *time.Time
	DeadLetterReason string
	Outbox           []CreateMailboxMessageParams
}

// CreateScheduleParams configures [Store.CreateSchedule].
type CreateScheduleParams struct {
	NamespaceID string
	ScheduleID  string
	AgentID     string
	CronExpr    string
	TimeZone    string
	Payload     any
	Enabled     bool
}

// PutAgentResult reports what [Store.PutAgent] changed.
type PutAgentResult struct {
	Agent   RunnableAgent
	Created bool
	Updated bool
}

// ListMailboxMessagesParams filters [Store.ListMailboxMessages].
type ListMailboxMessagesParams struct {
	NamespaceID string
	AgentID     string
	Status      MailboxMessageStatus
	Limit       int
}

// ListRunsParams filters [Store.ListRuns].
type ListRunsParams struct {
	NamespaceID string
	AgentID     string
	Status      string
	Limit       int
}

// NewID returns a random identifier with the given prefix.
func NewID(prefix string) string {
	var raw [10]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(fmt.Sprintf("server/store: could not generate id: %v", err))
	}
	return prefix + "_" + hex.EncodeToString(raw[:])
}
