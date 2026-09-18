package storage

import (
	"context"
	"fmt"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/knowledge"
)

// KnowledgeReadCapability adapts the SQLite repository to the read-only
// Knowledge port.
type KnowledgeReadCapability struct {
	repo *Repository
}

// NewKnowledgeReadCapability wires the Knowledge read port over one repository.
func NewKnowledgeReadCapability(repo *Repository) *KnowledgeReadCapability {
	return &KnowledgeReadCapability{repo: repo}
}

// GetEntity reads one entity.
func (c *KnowledgeReadCapability) GetEntity(ctx context.Context, query knowledge.GetEntityQuery) (domain.Entity, error) {
	return c.repo.GetEntity(ctx, query.EntityID, query.IncludeMentions)
}

// ListEntities lists entities in a scope.
func (c *KnowledgeReadCapability) ListEntities(ctx context.Context, query knowledge.ListEntitiesQuery) (domain.EntityPage, error) {
	return c.repo.ListEntities(ctx, EntityFilter{
		SpaceID:         query.SpaceID,
		State:           query.State,
		EntityType:      query.EntityType,
		Cursor:          query.Page.Cursor,
		Limit:           query.Page.EffectiveLimit(),
		IncludeMentions: query.IncludeMentions,
	})
}

// GetRelation reads one relation, optionally bound to a scope.
func (c *KnowledgeReadCapability) GetRelation(ctx context.Context, query knowledge.GetRelationQuery) (domain.MemoryRelation, error) {
	if query.SpaceID != "" {
		return c.repo.GetRelationInSpace(ctx, query.RelationID, query.SpaceID)
	}
	return c.repo.GetRelation(ctx, query.RelationID)
}

// ListRelations lists relations in a scope.
func (c *KnowledgeReadCapability) ListRelations(ctx context.Context, query knowledge.ListRelationsQuery) (domain.RelationPage, error) {
	return c.repo.ListRelations(ctx, RelationFilter{
		SpaceID:      query.SpaceID,
		State:        query.State,
		RelationType: query.RelationType,
		MemoryID:     query.MemoryID,
		RevisionID:   query.RevisionID,
		Direction:    query.Direction,
		Cursor:       query.Page.Cursor,
		Limit:        query.Page.EffectiveLimit(),
	})
}

// GetEntityFact reads one entity fact.
func (c *KnowledgeReadCapability) GetEntityFact(ctx context.Context, query knowledge.GetEntityFactQuery) (domain.EntityFact, error) {
	return c.repo.GetEntityFact(ctx, query.FactID)
}

// ListEntityFacts lists entity facts in a scope.
func (c *KnowledgeReadCapability) ListEntityFacts(ctx context.Context, query knowledge.ListEntityFactsQuery) (domain.EntityFactPage, error) {
	return c.repo.ListEntityFacts(ctx, FactFilter{
		SpaceID:         query.SpaceID,
		SubjectEntityID: query.SubjectEntityID,
		ObjectEntityID:  query.ObjectEntityID,
		Predicate:       query.Predicate,
		State:           query.State,
		Direction:       query.Direction,
		ReferenceTime:   query.ReferenceTime,
		Cursor:          query.Page.Cursor,
		Limit:           query.Page.EffectiveLimit(),
	})
}

// GetConflictCase reads one conflict case.
func (c *KnowledgeReadCapability) GetConflictCase(ctx context.Context, query knowledge.GetConflictCaseQuery) (domain.ConflictCase, error) {
	return c.repo.GetConflictCase(ctx, query.CaseID)
}

// ListConflictCases lists conflict cases in a scope.
func (c *KnowledgeReadCapability) ListConflictCases(ctx context.Context, query knowledge.ListConflictCasesQuery) (domain.ConflictCasePage, error) {
	return c.repo.ListConflictCases(ctx, ConflictCaseFilter{
		SpaceID: query.SpaceID,
		State:   query.State,
		Cursor:  query.Page.Cursor,
		Limit:   query.Page.EffectiveLimit(),
	})
}

// KnowledgeReviewCapability adapts the SQLite repository to the Knowledge review
// mutation port. It is not part of the initial Agent HTTP surface.
type KnowledgeReviewCapability struct {
	repo *Repository
}

// NewKnowledgeReviewCapability wires the Knowledge review port over one
// repository.
func NewKnowledgeReviewCapability(repo *Repository) *KnowledgeReviewCapability {
	return &KnowledgeReviewCapability{repo: repo}
}

// ReviewEntityProposal confirms or rejects one entity proposal.
func (c *KnowledgeReviewCapability) ReviewEntityProposal(ctx context.Context, identity gateway.WriteIdentity, proposalID string, params knowledge.ReviewParams) (domain.EntityProposal, bool, error) {
	confirm, err := reviewConfirmed(params.Decision)
	if err != nil {
		return domain.EntityProposal{}, false, err
	}
	review := ReviewEntityProposalParams{ReasonCode: params.ReasonCode, Note: params.Note}
	if confirm {
		return c.repo.ConfirmEntityProposal(ctx, gatewayWriteIdentity(identity), proposalID, review)
	}
	return c.repo.RejectEntityProposal(ctx, gatewayWriteIdentity(identity), proposalID, review)
}

// ReviewRelationProposal confirms or rejects one relation proposal.
func (c *KnowledgeReviewCapability) ReviewRelationProposal(ctx context.Context, identity gateway.WriteIdentity, proposalID string, params knowledge.ReviewParams) (domain.RelationProposal, bool, error) {
	confirm, err := reviewConfirmed(params.Decision)
	if err != nil {
		return domain.RelationProposal{}, false, err
	}
	review := ReviewRelationProposalParams{ReasonCode: params.ReasonCode, Note: params.Note}
	if confirm {
		return c.repo.ConfirmRelationProposal(ctx, gatewayWriteIdentity(identity), proposalID, review)
	}
	return c.repo.RejectRelationProposal(ctx, gatewayWriteIdentity(identity), proposalID, review)
}

// ReviewFactProposal confirms or rejects one fact proposal.
func (c *KnowledgeReviewCapability) ReviewFactProposal(ctx context.Context, identity gateway.WriteIdentity, proposalID string, params knowledge.ReviewParams) (domain.FactProposal, bool, error) {
	confirm, err := reviewConfirmed(params.Decision)
	if err != nil {
		return domain.FactProposal{}, false, err
	}
	review := ReviewFactProposalParams{ReasonCode: params.ReasonCode, Note: params.Note}
	if confirm {
		return c.repo.ConfirmFactProposal(ctx, gatewayWriteIdentity(identity), proposalID, review)
	}
	return c.repo.RejectFactProposal(ctx, gatewayWriteIdentity(identity), proposalID, review)
}

// ReviewEvolutionProposal confirms or rejects one evolution proposal.
func (c *KnowledgeReviewCapability) ReviewEvolutionProposal(ctx context.Context, identity gateway.WriteIdentity, proposalID string, params knowledge.ReviewParams) (domain.EvolutionProposal, bool, error) {
	confirm, err := reviewConfirmed(params.Decision)
	if err != nil {
		return domain.EvolutionProposal{}, false, err
	}
	review := ReviewEvolutionProposalParams{ReasonCode: params.ReasonCode, Note: params.Note}
	if confirm {
		return c.repo.ConfirmEvolutionProposal(ctx, gatewayWriteIdentity(identity), proposalID, review)
	}
	return c.repo.RejectEvolutionProposal(ctx, gatewayWriteIdentity(identity), proposalID, review)
}

// ReviewAggregateProposal confirms or rejects one aggregate proposal.
func (c *KnowledgeReviewCapability) ReviewAggregateProposal(ctx context.Context, identity gateway.WriteIdentity, proposalID string, params knowledge.ReviewParams) (domain.AggregateProposal, bool, error) {
	confirm, err := reviewConfirmed(params.Decision)
	if err != nil {
		return domain.AggregateProposal{}, false, err
	}
	review := ReviewAggregateProposalParams{ReasonCode: params.ReasonCode, Note: params.Note}
	if confirm {
		return c.repo.ConfirmAggregateProposal(ctx, gatewayWriteIdentity(identity), proposalID, review)
	}
	return c.repo.RejectAggregateProposal(ctx, gatewayWriteIdentity(identity), proposalID, review)
}

// ResolveConflict resolves one conflict case.
func (c *KnowledgeReviewCapability) ResolveConflict(ctx context.Context, identity gateway.WriteIdentity, caseID string, command domain.ConflictResolutionCommand) (domain.ConflictCase, bool, error) {
	return c.repo.ResolveConflict(ctx, gatewayWriteIdentity(identity), caseID, ResolveConflictParams(command))
}

func reviewConfirmed(decision knowledge.ReviewDecision) (bool, error) {
	switch decision {
	case knowledge.ReviewConfirm:
		return true, nil
	case knowledge.ReviewReject:
		return false, nil
	default:
		return false, fmt.Errorf("storage: review decision %q is invalid", decision)
	}
}
