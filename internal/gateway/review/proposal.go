package review

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// Operation names published by the proposal review surface. Each family names
// its confirm and reject operations separately so audit and idempotency
// identity never conflate one proposal family with another.
const (
	OperationEntityConfirm    gateway.OperationName = "review.entity_confirm"
	OperationEntityReject     gateway.OperationName = "review.entity_reject"
	OperationFactConfirm      gateway.OperationName = "review.fact_confirm"
	OperationFactReject       gateway.OperationName = "review.fact_reject"
	OperationRelationConfirm  gateway.OperationName = "review.relation_confirm"
	OperationRelationReject   gateway.OperationName = "review.relation_reject"
	OperationEvolutionConfirm gateway.OperationName = "review.evolution_confirm"
	OperationEvolutionReject  gateway.OperationName = "review.evolution_reject"
	OperationAggregateConfirm gateway.OperationName = "review.aggregate_confirm"
	OperationAggregateReject  gateway.OperationName = "review.aggregate_reject"
)

// MaxProposalTargets bounds the expected evolution target list.
const MaxProposalTargets = 16

// ProposalKind is the closed set of reviewable proposal families.
type ProposalKind string

const (
	// ProposalKindEntity covers entity, alias, and mention proposals.
	ProposalKindEntity ProposalKind = "entity"
	// ProposalKindFact covers typed entity fact proposals.
	ProposalKindFact ProposalKind = "fact"
	// ProposalKindRelation covers revision-level memory relation proposals.
	ProposalKindRelation ProposalKind = "relation"
	// ProposalKindEvolution covers duplicate, supplement, replacement, and
	// conflict evolution proposals.
	ProposalKindEvolution ProposalKind = "evolution"
	// ProposalKindAggregate covers scene, topic, and timeline aggregate proposals.
	ProposalKindAggregate ProposalKind = "aggregate"
)

// Valid reports whether the kind is one of the closed set.
func (k ProposalKind) Valid() bool {
	switch k {
	case ProposalKindEntity, ProposalKindFact, ProposalKindRelation, ProposalKindEvolution, ProposalKindAggregate:
		return true
	default:
		return false
	}
}

// Proposal is the reviewable union value. Exactly one field is set, matching
// Kind.
type Proposal struct {
	Kind      ProposalKind
	Entity    *domain.EntityProposal
	Fact      *domain.FactProposal
	Relation  *domain.RelationProposal
	Evolution *domain.EvolutionProposal
	Aggregate *domain.AggregateProposal
}

// NewEntityProposal wraps one entity-family proposal value.
func NewEntityProposal(value domain.EntityProposal) Proposal {
	return Proposal{Kind: ProposalKindEntity, Entity: &value}
}

// NewFactProposal wraps one fact-family proposal value.
func NewFactProposal(value domain.FactProposal) Proposal {
	return Proposal{Kind: ProposalKindFact, Fact: &value}
}

// NewRelationProposal wraps one relation-family proposal value.
func NewRelationProposal(value domain.RelationProposal) Proposal {
	return Proposal{Kind: ProposalKindRelation, Relation: &value}
}

// NewEvolutionProposal wraps one evolution-family proposal value.
func NewEvolutionProposal(value domain.EvolutionProposal) Proposal {
	return Proposal{Kind: ProposalKindEvolution, Evolution: &value}
}

// NewAggregateProposal wraps one aggregate-family proposal value.
func NewAggregateProposal(value domain.AggregateProposal) Proposal {
	return Proposal{Kind: ProposalKindAggregate, Aggregate: &value}
}

// Value returns the populated union member, or nil for an unknown kind.
func (p Proposal) Value() any {
	switch p.Kind {
	case ProposalKindEntity:
		return p.Entity
	case ProposalKindFact:
		return p.Fact
	case ProposalKindRelation:
		return p.Relation
	case ProposalKindEvolution:
		return p.Evolution
	case ProposalKindAggregate:
		return p.Aggregate
	default:
		return nil
	}
}

// ProposalPage is one bounded page of proposal values.
type ProposalPage struct {
	Items      []Proposal
	NextCursor *string
}

// ProposalService is the protocol-facing proposal review surface.
type ProposalService interface {
	ListProposals(context.Context, gateway.CallContext, ListProposalsQuery) (ProposalPage, error)
	GetProposal(context.Context, gateway.CallContext, GetProposalQuery) (Proposal, error)
	ConfirmProposal(context.Context, gateway.CallContext, ReviewProposalCommand) (ProposalReviewResult, error)
	RejectProposal(context.Context, gateway.CallContext, ReviewProposalCommand) (ProposalReviewResult, error)
}

// ListProposalsQuery bounds one proposal list read. Kind is required because
// proposal identifiers are only unique within one family.
type ListProposalsQuery struct {
	SpaceID     string
	Kind        ProposalKind
	ReviewState string
	Page        gateway.PageRequest
}

// GetProposalQuery reads one proposal by family and identifier.
type GetProposalQuery struct {
	Kind       ProposalKind
	ProposalID string
}

// ReviewProposalCommand is one caller-supplied proposal review. The
// evolution-specific fields are accepted only for the evolution family; the
// storage layer remains the authority for the exact participant fence.
type ReviewProposalCommand struct {
	Kind             ProposalKind
	ProposalID       string
	ReasonCode       string
	Note             *string
	ExpectedTargets  []domain.EvolutionTarget
	ReplacementMode  *string
	SupersedeTargets bool
}

// ProposalReviewResult is the terminal state of one reviewed proposal.
type ProposalReviewResult struct {
	gateway.WriteResult
	Proposal Proposal
}

// proposalOperation maps one family and review outcome to its stable operation
// name.
func proposalOperation(kind ProposalKind, confirm bool) gateway.OperationName {
	switch kind {
	case ProposalKindEntity:
		if confirm {
			return OperationEntityConfirm
		}
		return OperationEntityReject
	case ProposalKindFact:
		if confirm {
			return OperationFactConfirm
		}
		return OperationFactReject
	case ProposalKindRelation:
		if confirm {
			return OperationRelationConfirm
		}
		return OperationRelationReject
	case ProposalKindEvolution:
		if confirm {
			return OperationEvolutionConfirm
		}
		return OperationEvolutionReject
	case ProposalKindAggregate:
		if confirm {
			return OperationAggregateConfirm
		}
		return OperationAggregateReject
	default:
		return ""
	}
}
