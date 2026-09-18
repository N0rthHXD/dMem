// Package knowledge defines the protocol-independent contracts for entity,
// relation, fact, proposal review, and conflict resolution semantics. Read and
// review surfaces are separate so a protocol can expose graph reads without
// gaining mutation or review authority.
package knowledge

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// Operation names published by the Knowledge review surface. They are stable
// and transport-independent, so idempotency never depends on an HTTP route.
const (
	OperationReviewEntityProposal    gateway.OperationName = "knowledge.review_entity"
	OperationReviewRelationProposal  gateway.OperationName = "knowledge.review_relation"
	OperationReviewFactProposal      gateway.OperationName = "knowledge.review_fact"
	OperationReviewEvolutionProposal gateway.OperationName = "knowledge.review_evolution"
	OperationReviewAggregateProposal gateway.OperationName = "knowledge.review_aggregate"
	OperationResolveConflict         gateway.OperationName = "knowledge.resolve_conflict"
)

// Reader is the read-only Knowledge surface.
type Reader interface {
	GetEntity(context.Context, gateway.CallContext, GetEntityQuery) (domain.Entity, error)
	ListEntities(context.Context, gateway.CallContext, ListEntitiesQuery) (domain.EntityPage, error)
	GetRelation(context.Context, gateway.CallContext, GetRelationQuery) (domain.MemoryRelation, error)
	ListRelations(context.Context, gateway.CallContext, ListRelationsQuery) (domain.RelationPage, error)
	GetEntityFact(context.Context, gateway.CallContext, GetEntityFactQuery) (domain.EntityFact, error)
	ListEntityFacts(context.Context, gateway.CallContext, ListEntityFactsQuery) (domain.EntityFactPage, error)
	GetConflictCase(context.Context, gateway.CallContext, GetConflictCaseQuery) (domain.ConflictCase, error)
	ListConflictCases(context.Context, gateway.CallContext, ListConflictCasesQuery) (domain.ConflictCasePage, error)
}

// Reviewer is the mutation surface for proposal review and conflict resolution.
// It is never exposed through the initial Agent HTTP API.
type Reviewer interface {
	ReviewEntityProposal(context.Context, gateway.CallContext, ReviewProposalCommand) (ReviewEntityResult, error)
	ReviewRelationProposal(context.Context, gateway.CallContext, ReviewProposalCommand) (ReviewRelationResult, error)
	ReviewFactProposal(context.Context, gateway.CallContext, ReviewProposalCommand) (ReviewFactResult, error)
	ReviewEvolutionProposal(context.Context, gateway.CallContext, ReviewProposalCommand) (ReviewEvolutionResult, error)
	ReviewAggregateProposal(context.Context, gateway.CallContext, ReviewProposalCommand) (ReviewAggregateResult, error)
	ResolveConflict(context.Context, gateway.CallContext, ResolveConflictCommand) (ResolveConflictResult, error)
}

// ReviewEntityResult is the outcome of reviewing one entity proposal.
type ReviewEntityResult struct {
	gateway.WriteResult
	Proposal domain.EntityProposal
}

// ReviewRelationResult is the outcome of reviewing one relation proposal.
type ReviewRelationResult struct {
	gateway.WriteResult
	Proposal domain.RelationProposal
}

// ReviewFactResult is the outcome of reviewing one fact proposal.
type ReviewFactResult struct {
	gateway.WriteResult
	Proposal domain.FactProposal
}

// ReviewEvolutionResult is the outcome of reviewing one evolution proposal.
type ReviewEvolutionResult struct {
	gateway.WriteResult
	Proposal domain.EvolutionProposal
}

// ReviewAggregateResult is the outcome of reviewing one aggregate proposal.
type ReviewAggregateResult struct {
	gateway.WriteResult
	Proposal domain.AggregateProposal
}

// ResolveConflictResult is the outcome of resolving one conflict case.
type ResolveConflictResult struct {
	gateway.WriteResult
	Case domain.ConflictCase
}

// GetEntityQuery reads one entity, optionally narrowed to a scope.
type GetEntityQuery struct {
	EntityID        string
	SpaceID         string
	IncludeMentions bool
}

// ListEntitiesQuery lists entities in a scope.
type ListEntitiesQuery struct {
	SpaceID         string
	State           string
	EntityType      string
	IncludeMentions bool
	Page            gateway.PageRequest
}

// GetRelationQuery reads one relation.
type GetRelationQuery struct {
	RelationID string
	SpaceID    string
}

// ListRelationsQuery lists relations in a scope.
type ListRelationsQuery struct {
	SpaceID      string
	State        string
	RelationType string
	MemoryID     string
	RevisionID   string
	Direction    string
	Page         gateway.PageRequest
}

// GetEntityFactQuery reads one entity fact, optionally narrowed to a scope.
type GetEntityFactQuery struct {
	FactID  string
	SpaceID string
}

// ListEntityFactsQuery lists entity facts in a scope.
type ListEntityFactsQuery struct {
	SpaceID         string
	SubjectEntityID string
	ObjectEntityID  string
	Predicate       string
	State           string
	Direction       string
	ReferenceTime   string
	Page            gateway.PageRequest
}

// GetConflictCaseQuery reads one conflict case.
type GetConflictCaseQuery struct {
	CaseID string
}

// ListConflictCasesQuery lists conflict cases in a scope.
type ListConflictCasesQuery struct {
	SpaceID string
	State   string
	Page    gateway.PageRequest
}

// ReviewDecision is the terminal decision applied to a proposal.
type ReviewDecision string

const (
	// ReviewConfirm accepts a proposal.
	ReviewConfirm ReviewDecision = "confirm"
	// ReviewReject rejects a proposal.
	ReviewReject ReviewDecision = "reject"
)

// Valid reports whether the decision is one of the defined values.
func (d ReviewDecision) Valid() bool {
	return d == ReviewConfirm || d == ReviewReject
}

// ReviewProposalCommand reviews one proposal with a terminal decision.
type ReviewProposalCommand struct {
	ProposalID string
	Decision   ReviewDecision
	ReasonCode string
	Note       *string
}

// ResolveConflictCommand resolves one conflict case.
type ResolveConflictCommand struct {
	CaseID string
	domain.ConflictResolutionCommand
}
