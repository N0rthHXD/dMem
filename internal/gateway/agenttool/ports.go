package agenttool

import (
	"context"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

const CapabilityAuthorityWrite = "authority_write"

// Store is the persistence capability owned by the Agent Tool consumer.
type Store interface {
	CommitMemory(context.Context, gateway.WriteIdentity, CommitParams) (CommitRecord, bool, error)
	AppendFeedback(context.Context, gateway.WriteIdentity, FeedbackParams) (FeedbackRecord, bool, error)
	ForgetMemory(context.Context, gateway.WriteIdentity, ForgetParams) (ForgetRecord, bool, error)
}

// RecallPort adapts the shared recall application service without coupling this
// gateway module to a sibling gateway package.
type RecallPort interface {
	Recall(context.Context, gateway.CallContext, RecallParams) (RecallRecord, error)
}

type Authorizer interface {
	Authorize(context.Context, gateway.CallContext, AuthorizationRequest) (AuthorizationDecision, error)
}

type AuthorizationRequest struct {
	Capability string
	ScopeIDs   []string
}

type AuthorizationDecision struct {
	Allowed    bool
	ReasonCode string
}

type RecallParams struct {
	OperationID     string
	Query           string
	ScopeIDs        []string
	Intent          domain.RecallIntent
	EntityHints     []string
	ReferenceTime   *time.Time
	Limit           int
	Cursor          string
	IncludeEvidence bool
}

type RecallRecord struct {
	OperationID           string
	Memories              []RecallMemory
	RequestedCapabilities []Capability
	ExecutedCapabilities  []Capability
	DegradationReasons    []string
	NextCursor            *string
}

type RecallMemory struct {
	MemoryID   string
	RevisionID string
	Kind       domain.MemoryKind
	Content    string
	ValidFrom  *time.Time
	ValidUntil *time.Time
	Confidence float64
	Evidence   []domain.RevisionEvidence
}

type CommitParams struct {
	ScopeID   string
	Source    Source
	Memories  []MemorySuggestion
	Entities  []EntitySuggestion
	Facts     []FactSuggestion
	Relations []RelationSuggestion
}

type CommitRecord struct {
	OperationID   string
	SourceEventID string
	Memories      []CommitOutcome
	Entities      []CommitOutcome
	Facts         []CommitOutcome
	Relations     []CommitOutcome
}

type FeedbackParams struct {
	ScopeID           string
	MemoryID          string
	RevisionID        string
	Kind              FeedbackKind
	Reason            string
	RecallOperationID string
}

type FeedbackRecord struct {
	OperationID string
	FeedbackID  string
	MemoryID    string
	RevisionID  string
	Kind        FeedbackKind
}

type ForgetParams struct {
	ScopeID            string
	MemoryID           string
	ExpectedRevisionID string
	Reason             string
}

type ForgetRecord struct {
	OperationID string
	MemoryID    string
	RevisionID  string
	State       string
}
