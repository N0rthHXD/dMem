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

// GetEntity reads one entity, optionally narrowed to a scope.
func (c *KnowledgeReadCapability) GetEntity(ctx context.Context, query knowledge.GetEntityQuery) (domain.Entity, error) {
	if query.SpaceID != "" {
		entity, err := c.repo.GetEntityInSpace(ctx, query.EntityID, query.SpaceID, query.IncludeMentions)
		if err != nil {
			return domain.Entity{}, gatewayError(err)
		}
		return entity, nil
	}
	entity, err := c.repo.GetEntity(ctx, query.EntityID, query.IncludeMentions)
	if err != nil {
		return domain.Entity{}, gatewayError(err)
	}
	return entity, nil
}

// ListEntities lists entities in a scope.
func (c *KnowledgeReadCapability) ListEntities(ctx context.Context, query knowledge.ListEntitiesQuery) (domain.EntityPage, error) {
	page, err := c.repo.ListEntities(ctx, EntityFilter{
		SpaceID:         query.SpaceID,
		State:           query.State,
		EntityType:      query.EntityType,
		Cursor:          query.Page.Cursor,
		Limit:           query.Page.EffectiveLimit(),
		IncludeMentions: query.IncludeMentions,
	})
	if err != nil {
		return domain.EntityPage{}, gatewayError(err)
	}
	return page, nil
}

// GetRelation reads one relation, optionally bound to a scope.
func (c *KnowledgeReadCapability) GetRelation(ctx context.Context, query knowledge.GetRelationQuery) (domain.MemoryRelation, error) {
	var (
		relation domain.MemoryRelation
		err      error
	)
	if query.SpaceID != "" {
		relation, err = c.repo.GetRelationInSpace(ctx, query.RelationID, query.SpaceID)
	} else {
		relation, err = c.repo.GetRelation(ctx, query.RelationID)
	}
	if err != nil {
		return domain.MemoryRelation{}, gatewayError(err)
	}
	return relation, nil
}

// ListRelations lists relations in a scope.
func (c *KnowledgeReadCapability) ListRelations(ctx context.Context, query knowledge.ListRelationsQuery) (domain.RelationPage, error) {
	page, err := c.repo.ListRelations(ctx, RelationFilter{
		SpaceID:      query.SpaceID,
		State:        query.State,
		RelationType: query.RelationType,
		MemoryID:     query.MemoryID,
		RevisionID:   query.RevisionID,
		Direction:    query.Direction,
		Cursor:       query.Page.Cursor,
		Limit:        query.Page.EffectiveLimit(),
	})
	if err != nil {
		return domain.RelationPage{}, gatewayError(err)
	}
	return page, nil
}

// GetEntityFact reads one entity fact.
func (c *KnowledgeReadCapability) GetEntityFact(ctx context.Context, query knowledge.GetEntityFactQuery) (domain.EntityFact, error) {
	fact, err := c.repo.GetEntityFact(ctx, query.FactID)
	if err != nil {
		return domain.EntityFact{}, gatewayError(err)
	}
	return fact, nil
}

// ListEntityFacts lists entity facts in a scope.
func (c *KnowledgeReadCapability) ListEntityFacts(ctx context.Context, query knowledge.ListEntityFactsQuery) (domain.EntityFactPage, error) {
	page, err := c.repo.ListEntityFacts(ctx, FactFilter{
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
	if err != nil {
		return domain.EntityFactPage{}, gatewayError(err)
	}
	return page, nil
}

// GetConflictCase reads one conflict case.
func (c *KnowledgeReadCapability) GetConflictCase(ctx context.Context, query knowledge.GetConflictCaseQuery) (domain.ConflictCase, error) {
	found, err := c.repo.GetConflictCase(ctx, query.CaseID)
	if err != nil {
		return domain.ConflictCase{}, gatewayError(err)
	}
	return found, nil
}

// ListConflictCases lists conflict cases in a scope.
func (c *KnowledgeReadCapability) ListConflictCases(ctx context.Context, query knowledge.ListConflictCasesQuery) (domain.ConflictCasePage, error) {
	page, err := c.repo.ListConflictCases(ctx, ConflictCaseFilter{
		SpaceID: query.SpaceID,
		State:   query.State,
		Cursor:  query.Page.Cursor,
		Limit:   query.Page.EffectiveLimit(),
	})
	if err != nil {
		return domain.ConflictCasePage{}, gatewayError(err)
	}
	return page, nil
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
	var (
		proposal domain.EntityProposal
		replay   bool
	)
	if confirm {
		proposal, replay, err = c.repo.ConfirmEntityProposal(ctx, gatewayWriteIdentity(identity), proposalID, review)
	} else {
		proposal, replay, err = c.repo.RejectEntityProposal(ctx, gatewayWriteIdentity(identity), proposalID, review)
	}
	if err != nil {
		return domain.EntityProposal{}, replay, gatewayError(err)
	}
	return proposal, replay, nil
}

// ReviewRelationProposal confirms or rejects one relation proposal.
func (c *KnowledgeReviewCapability) ReviewRelationProposal(ctx context.Context, identity gateway.WriteIdentity, proposalID string, params knowledge.ReviewParams) (domain.RelationProposal, bool, error) {
	confirm, err := reviewConfirmed(params.Decision)
	if err != nil {
		return domain.RelationProposal{}, false, err
	}
	review := ReviewRelationProposalParams{ReasonCode: params.ReasonCode, Note: params.Note}
	var (
		proposal domain.RelationProposal
		replay   bool
	)
	if confirm {
		proposal, replay, err = c.repo.ConfirmRelationProposal(ctx, gatewayWriteIdentity(identity), proposalID, review)
	} else {
		proposal, replay, err = c.repo.RejectRelationProposal(ctx, gatewayWriteIdentity(identity), proposalID, review)
	}
	if err != nil {
		return domain.RelationProposal{}, replay, gatewayError(err)
	}
	return proposal, replay, nil
}

// ReviewFactProposal confirms or rejects one fact proposal.
func (c *KnowledgeReviewCapability) ReviewFactProposal(ctx context.Context, identity gateway.WriteIdentity, proposalID string, params knowledge.ReviewParams) (domain.FactProposal, bool, error) {
	confirm, err := reviewConfirmed(params.Decision)
	if err != nil {
		return domain.FactProposal{}, false, err
	}
	review := ReviewFactProposalParams{ReasonCode: params.ReasonCode, Note: params.Note}
	var (
		proposal domain.FactProposal
		replay   bool
	)
	if confirm {
		proposal, replay, err = c.repo.ConfirmFactProposal(ctx, gatewayWriteIdentity(identity), proposalID, review)
	} else {
		proposal, replay, err = c.repo.RejectFactProposal(ctx, gatewayWriteIdentity(identity), proposalID, review)
	}
	if err != nil {
		return domain.FactProposal{}, replay, gatewayError(err)
	}
	return proposal, replay, nil
}

// ReviewEvolutionProposal confirms or rejects one evolution proposal.
func (c *KnowledgeReviewCapability) ReviewEvolutionProposal(ctx context.Context, identity gateway.WriteIdentity, proposalID string, params knowledge.ReviewParams) (domain.EvolutionProposal, bool, error) {
	confirm, err := reviewConfirmed(params.Decision)
	if err != nil {
		return domain.EvolutionProposal{}, false, err
	}
	review := ReviewEvolutionProposalParams{ReasonCode: params.ReasonCode, Note: params.Note}
	var (
		proposal domain.EvolutionProposal
		replay   bool
	)
	if confirm {
		proposal, replay, err = c.repo.ConfirmEvolutionProposal(ctx, gatewayWriteIdentity(identity), proposalID, review)
	} else {
		proposal, replay, err = c.repo.RejectEvolutionProposal(ctx, gatewayWriteIdentity(identity), proposalID, review)
	}
	if err != nil {
		return domain.EvolutionProposal{}, replay, gatewayError(err)
	}
	return proposal, replay, nil
}

// ReviewAggregateProposal confirms or rejects one aggregate proposal.
func (c *KnowledgeReviewCapability) ReviewAggregateProposal(ctx context.Context, identity gateway.WriteIdentity, proposalID string, params knowledge.ReviewParams) (domain.AggregateProposal, bool, error) {
	confirm, err := reviewConfirmed(params.Decision)
	if err != nil {
		return domain.AggregateProposal{}, false, err
	}
	review := ReviewAggregateProposalParams{ReasonCode: params.ReasonCode, Note: params.Note}
	var (
		proposal domain.AggregateProposal
		replay   bool
	)
	if confirm {
		proposal, replay, err = c.repo.ConfirmAggregateProposal(ctx, gatewayWriteIdentity(identity), proposalID, review)
	} else {
		proposal, replay, err = c.repo.RejectAggregateProposal(ctx, gatewayWriteIdentity(identity), proposalID, review)
	}
	if err != nil {
		return domain.AggregateProposal{}, replay, gatewayError(err)
	}
	return proposal, replay, nil
}

// ResolveConflict resolves one conflict case.
func (c *KnowledgeReviewCapability) ResolveConflict(ctx context.Context, identity gateway.WriteIdentity, caseID string, command domain.ConflictResolutionCommand) (domain.ConflictCase, bool, error) {
	resolved, replay, err := c.repo.ResolveConflict(ctx, gatewayWriteIdentity(identity), caseID, ResolveConflictParams(command))
	if err != nil {
		return domain.ConflictCase{}, replay, gatewayError(err)
	}
	return resolved, replay, nil
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
