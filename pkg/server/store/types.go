package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

type AgentRole string

const (
	AgentRolePrimary AgentRole = "primary"
	AgentRoleWorker  AgentRole = "worker"
)

type AgentDesiredState string

const (
	AgentDesiredStateRunning AgentDesiredState = "running"
	AgentDesiredStatePaused  AgentDesiredState = "paused"
	AgentDesiredStateStopped AgentDesiredState = "stopped"
)

type MailboxMessageStatus string

const (
	MailboxMessageStatusQueued     MailboxMessageStatus = "queued"
	MailboxMessageStatusLeased     MailboxMessageStatus = "leased"
	MailboxMessageStatusCompleted  MailboxMessageStatus = "completed"
	MailboxMessageStatusDeadLetter MailboxMessageStatus = "dead_letter"
)

type RunStatus string

const (
	RunStatusRunning RunStatus = "running"
)

const DefaultAgentStepTimeout = 5 * time.Minute

type JSONEnvelope struct {
	Version int             `json:"version"`
	Type    string          `json:"type,omitempty"`
	Body    json.RawMessage `json:"body"`
}

type Namespace struct {
	ID         string
	Name       string
	LimitsJSON string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

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

type RunnableAgent struct {
	AgentRecord
	SystemPrompt string
}

type AgentPromptVersion struct {
	NamespaceID      string
	ID               string
	AgentID          string
	Version          int
	Prompt           string
	ActionSchemaJSON string
	CreatedAt        time.Time
}

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

type RunRecord struct {
	NamespaceID       string
	ID                string
	MessageID         string
	AgentID           string
	TriggerID         string
	Status            string
	StartedAt         time.Time
	FinishedAt        *time.Time
	Duration          time.Duration
	CWD               string
	UsageCachedTokens int
	FinishThought     string
	ValueJSON         string
	Error             string
	TriggerPrompt     string
	SystemPrompt      string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

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
	UsageCachedTokens     int
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

type NamespaceSnapshot struct {
	Namespace Namespace
	Agents    []RunnableAgent
	Schedules []ScheduleRecord
	Mailbox   MailboxSummary
}

type MailboxSummary struct {
	Queued     int
	Leased     int
	DeadLetter int
	Completed  int
}

type CreateNamespaceParams struct {
	ID     string
	Name   string
	Limits any
}

type PutNamespaceResult struct {
	Namespace Namespace
	Created   bool
	Updated   bool
}

type ListAgentsParams struct {
	NamespaceID string
}

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

type UpdateAgentPromptParams struct {
	NamespaceID  string
	AgentID      string
	Prompt       string
	ActionSchema any
}

type UpdateAgentDesiredStateParams struct {
	NamespaceID  string
	AgentID      string
	DesiredState AgentDesiredState
}

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

type ClaimMessageParams struct {
	NamespaceID   string
	AgentID       string
	LeaseOwner    string
	LeaseDuration time.Duration
	SystemPrompt  string
}

type ClaimedMailboxMessage struct {
	Message MailboxMessageRecord
	Run     RunRecord
}

type CompleteRunParams struct {
	NamespaceID       string
	RunID             string
	MessageID         string
	Status            string
	FinishedAt        time.Time
	Duration          time.Duration
	CWD               string
	UsageCachedTokens int
	FinishThought     string
	Value             any
	Error             string
	RetryAt           *time.Time
	DeadLetterReason  string
	Outbox            []CreateMailboxMessageParams
}

type CreateScheduleParams struct {
	NamespaceID string
	ScheduleID  string
	AgentID     string
	CronExpr    string
	TimeZone    string
	Payload     any
	Enabled     bool
}

type PutAgentResult struct {
	Agent   RunnableAgent
	Created bool
	Updated bool
}

type ListMailboxMessagesParams struct {
	NamespaceID string
	AgentID     string
	Status      MailboxMessageStatus
	Limit       int
}

type ListRunsParams struct {
	NamespaceID string
	AgentID     string
	Status      string
	Limit       int
}

func NewID(prefix string) string {
	var raw [10]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(fmt.Sprintf("server/store: could not generate id: %v", err))
	}
	return prefix + "_" + hex.EncodeToString(raw[:])
}
