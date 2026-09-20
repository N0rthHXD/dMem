// Package agenttool defines the protocol-neutral Agent Tool Route contract.
package agenttool

import (
	"context"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

const (
	ContractVersion = "dmem-agent-tool-v1"

	OperationRecall       gateway.OperationName = "agent_tool.recall"
	OperationMemoryCommit gateway.OperationName = "agent_tool.memory_commit"
	OperationFeedback     gateway.OperationName = "agent_tool.feedback"
	OperationForget       gateway.OperationName = "agent_tool.forget"
)

const (
	MaxQueryRunes          = 4096
	MaxScopeIDs            = 32
	MaxEntityHints         = 32
	MaxRecallLimit         = 100
	DefaultRecallLimit     = 10
	MaxMemories            = 32
	MaxContentRunes        = 4096
	MaxSearchHints         = 16
	MaxSearchHintRunes     = 256
	MaxEntities            = 256
	MaxAliases             = 16
	MaxFacts               = 256
	MaxRelations           = 256
	MaxReasonRunes         = 1024
	MaxMetadataKeys        = 32
	MaxMetadataBytes       = 16 << 10
	MaxMetadataDepth       = 4
	MaxEvidencePerMemory   = 64
	MaxIdentifierRunes     = 128
	MaxSourceIdentityRunes = 256
)

type Service interface {
	Recall(context.Context, gateway.CallContext, RecallCommand) (RecallResult, error)
	MemoryCommit(context.Context, gateway.CallContext, MemoryCommitCommand) (MemoryCommitResult, error)
	Feedback(context.Context, gateway.CallContext, FeedbackCommand) (FeedbackResult, error)
	Forget(context.Context, gateway.CallContext, ForgetCommand) (ForgetResult, error)
}

type RecallCommand struct {
	Query         string
	ScopeIDs      []string
	Intent        domain.RecallIntent
	EntityHints   []string
	ReferenceTime *time.Time
	Limit         int
	Cursor        string
}

type RecallResult struct {
	Memories             []RecalledMemory
	DegradedCapabilities []Capability
	NextCursor           *string
}

type RecalledMemory struct {
	MemoryID   string
	RevisionID string
	Kind       domain.MemoryKind
	Content    string
	ValidFrom  *time.Time
	ValidUntil *time.Time
	Confidence float64
	Evidence   []EvidenceReference
}

type EvidenceReference struct {
	EvidenceID    string
	EventID       string
	Role          domain.EvidenceRole
	EventTime     *time.Time
	EventSequence *int64
	StartOffset   *int64
	EndOffset     *int64
}

type Capability string

const (
	CapabilityKeyword  Capability = "keyword"
	CapabilityGraph    Capability = "graph"
	CapabilitySemantic Capability = "semantic"
)

type MemoryCommitCommand struct {
	ScopeID   string
	Source    Source
	Memories  []MemorySuggestion
	Entities  []EntitySuggestion
	Facts     []FactSuggestion
	Relations []RelationSuggestion
}

type Source struct {
	Type           string
	Name           string
	ConversationID string
	TurnID         string
	ToolCallID     string
	OccurredAt     time.Time
	Metadata       map[string]any
}

type MemorySuggestion struct {
	LocalID     string
	Kind        domain.MemoryKind
	Content     string
	Sensitivity Sensitivity
	Confidence  float64
	SearchHints []string
	ValidFrom   *time.Time
	ValidUntil  *time.Time
}

type Sensitivity string

const (
	SensitivityNormal    Sensitivity = "normal"
	SensitivitySensitive Sensitivity = "sensitive"
)

type EntitySuggestion struct {
	LocalID    string
	Type       EntityType
	Name       string
	Aliases    []string
	Confidence float64
}

type EntityType string

const (
	EntityPerson       EntityType = "person"
	EntityOrganization EntityType = "organization"
	EntityPlace        EntityType = "place"
	EntityProject      EntityType = "project"
	EntityProduct      EntityType = "product"
	EntityEvent        EntityType = "event"
	EntityConcept      EntityType = "concept"
	EntityOther        EntityType = "other"
)

type EntityReference struct {
	LocalID  string
	EntityID string
}

type FactObject struct {
	Entity       *EntityReference
	StringValue  *string
	NumberValue  *float64
	BooleanValue *bool
}

type FactSuggestion struct {
	LocalID        string
	Subject        EntityReference
	Predicate      string
	Object         FactObject
	EvidenceMemory string
	Confidence     float64
	ValidFrom      *time.Time
	ValidUntil     *time.Time
}

type MemoryReference struct {
	LocalID    string
	MemoryID   string
	RevisionID string
}

type RelationType string

const (
	RelationSupports    RelationType = "supports"
	RelationContradicts RelationType = "contradicts"
	RelationSupersedes  RelationType = "supersedes"
	RelationDerivedFrom RelationType = "derived_from"
)

type RelationSuggestion struct {
	LocalID        string
	Type           RelationType
	Source         MemoryReference
	Target         MemoryReference
	EvidenceMemory string
	Confidence     float64
}

type CommitStatus string

const (
	CommitAuthority    CommitStatus = "authority"
	CommitDeduplicated CommitStatus = "deduplicated"
	CommitProposal     CommitStatus = "proposal"
	CommitRejected     CommitStatus = "rejected"
)

type MemoryCommitResult struct {
	gateway.WriteResult
	SourceEventID string
	Memories      []CommitOutcome
	Entities      []CommitOutcome
	Facts         []CommitOutcome
	Relations     []CommitOutcome
}

type CommitOutcome struct {
	LocalID    string
	Status     CommitStatus
	MemoryID   string
	RevisionID string
	ProposalID string
	ReasonCode string
}

type FeedbackKind string

const (
	FeedbackUsed      FeedbackKind = "used"
	FeedbackNotUsed   FeedbackKind = "not_used"
	FeedbackIncorrect FeedbackKind = "incorrect"
	FeedbackOutdated  FeedbackKind = "outdated"
	FeedbackDuplicate FeedbackKind = "duplicate"
)

type FeedbackCommand struct {
	ScopeID           string
	MemoryID          string
	RevisionID        string
	Kind              FeedbackKind
	Reason            string
	RecallOperationID string
}

type FeedbackResult struct {
	gateway.WriteResult
	FeedbackID string
	MemoryID   string
	RevisionID string
	Kind       FeedbackKind
}

type ForgetCommand struct {
	ScopeID            string
	MemoryID           string
	ExpectedRevisionID string
	Reason             string
}

type ForgetResult struct {
	gateway.WriteResult
	MemoryID   string
	RevisionID string
	State      string
}
