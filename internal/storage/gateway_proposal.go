package storage

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/review"
)

// ProposalCapability adapts the SQLite proposal review routines of every
// reviewable family to the proposal review module's store port. It translates
// storage failures into stable gateway error codes while retaining the original
// cause; the review state machines and revision fences stay in storage.
type ProposalCapability struct {
	repo *Repository
}

// NewProposalCapability wires the proposal review store port over one
// repository.
func NewProposalCapability(repo *Repository) *ProposalCapability {
	return &ProposalCapability{repo: repo}
}

// ListProposals returns one bounded page of proposals of exactly one family.
func (c *ProposalCapability) ListProposals(ctx context.Context, filter review.ProposalFilter) (review.ProposalPage, error) {
	switch filter.Kind {
	case review.ProposalKindEntity:
		page, err := c.repo.ListEntityProposals(ctx, EntityProposalFilter{SpaceID: filter.SpaceID, ReviewState: filter.ReviewState, Limit: filter.Limit, Cursor: filter.Cursor})
		if err != nil {
			return review.ProposalPage{}, gatewayError(err)
		}
		items := make([]review.Proposal, 0, len(page.Items))
		for _, item := range page.Items {
			items = append(items, review.NewEntityProposal(item))
		}
		return review.ProposalPage{Items: items, NextCursor: page.NextCursor}, nil
	case review.ProposalKindFact:
		page, err := c.repo.ListFactProposals(ctx, FactProposalFilter{SpaceID: filter.SpaceID, ReviewState: filter.ReviewState, Limit: filter.Limit, Cursor: filter.Cursor})
		if err != nil {
			return review.ProposalPage{}, gatewayError(err)
		}
		items := make([]review.Proposal, 0, len(page.Items))
		for _, item := range page.Items {
			items = append(items, review.NewFactProposal(item))
		}
		return review.ProposalPage{Items: items, NextCursor: page.NextCursor}, nil
	case review.ProposalKindRelation:
		page, err := c.repo.ListRelationProposals(ctx, RelationProposalFilter{SpaceID: filter.SpaceID, ReviewState: filter.ReviewState, Limit: filter.Limit, Cursor: filter.Cursor})
		if err != nil {
			return review.ProposalPage{}, gatewayError(err)
		}
		items := make([]review.Proposal, 0, len(page.Items))
		for _, item := range page.Items {
			items = append(items, review.NewRelationProposal(item))
		}
		return review.ProposalPage{Items: items, NextCursor: page.NextCursor}, nil
	case review.ProposalKindEvolution:
		page, err := c.repo.ListEvolutionProposals(ctx, EvolutionProposalFilter{SpaceID: filter.SpaceID, ReviewState: filter.ReviewState, Limit: filter.Limit, Cursor: filter.Cursor})
		if err != nil {
			return review.ProposalPage{}, gatewayError(err)
		}
		items := make([]review.Proposal, 0, len(page.Items))
		for _, item := range page.Items {
			items = append(items, review.NewEvolutionProposal(item))
		}
		return review.ProposalPage{Items: items, NextCursor: page.NextCursor}, nil
	case review.ProposalKindAggregate:
		page, err := c.repo.ListAggregateProposals(ctx, AggregateProposalFilter{SpaceID: filter.SpaceID, ReviewState: filter.ReviewState, Limit: filter.Limit, Cursor: filter.Cursor})
		if err != nil {
			return review.ProposalPage{}, gatewayError(err)
		}
		items := make([]review.Proposal, 0, len(page.Items))
		for _, item := range page.Items {
			items = append(items, review.NewAggregateProposal(item))
		}
		return review.ProposalPage{Items: items, NextCursor: page.NextCursor}, nil
	default:
		return review.ProposalPage{}, gateway.NewError(gateway.ErrorInvalidInput, "proposal_type is unsupported")
	}
}

// GetProposal returns one proposal by family and identifier.
func (c *ProposalCapability) GetProposal(ctx context.Context, kind review.ProposalKind, proposalID string) (review.Proposal, error) {
	switch kind {
	case review.ProposalKindEntity:
		value, err := c.repo.GetEntityProposal(ctx, proposalID)
		if err != nil {
			return review.Proposal{}, gatewayError(err)
		}
		return review.NewEntityProposal(value), nil
	case review.ProposalKindFact:
		value, err := c.repo.GetFactProposal(ctx, proposalID)
		if err != nil {
			return review.Proposal{}, gatewayError(err)
		}
		return review.NewFactProposal(value), nil
	case review.ProposalKindRelation:
		value, err := c.repo.GetRelationProposal(ctx, proposalID)
		if err != nil {
			return review.Proposal{}, gatewayError(err)
		}
		return review.NewRelationProposal(value), nil
	case review.ProposalKindEvolution:
		value, err := c.repo.GetEvolutionProposal(ctx, proposalID)
		if err != nil {
			return review.Proposal{}, gatewayError(err)
		}
		return review.NewEvolutionProposal(value), nil
	case review.ProposalKindAggregate:
		value, err := c.repo.GetAggregateProposal(ctx, proposalID)
		if err != nil {
			return review.Proposal{}, gatewayError(err)
		}
		return review.NewAggregateProposal(value), nil
	default:
		return review.Proposal{}, gateway.NewError(gateway.ErrorInvalidInput, "proposal_type is unsupported")
	}
}

// ReviewProposal confirms or rejects one pending proposal.
func (c *ProposalCapability) ReviewProposal(ctx context.Context, identity gateway.WriteIdentity, params review.ReviewProposalParams) (review.Proposal, string, bool, error) {
	storageIdentity := gatewayWriteIdentity(identity)
	switch params.Kind {
	case review.ProposalKindEntity:
		reviewParams := ReviewEntityProposalParams{ReasonCode: params.ReasonCode, Note: params.Note}
		if params.Confirm {
			value, operationID, replay, err := c.repo.ConfirmEntityProposal(ctx, storageIdentity, params.ProposalID, reviewParams)
			if err != nil {
				return review.Proposal{}, "", replay, gatewayError(err)
			}
			return review.NewEntityProposal(value), operationID, replay, nil
		}
		value, operationID, replay, err := c.repo.RejectEntityProposal(ctx, storageIdentity, params.ProposalID, reviewParams)
		if err != nil {
			return review.Proposal{}, "", replay, gatewayError(err)
		}
		return review.NewEntityProposal(value), operationID, replay, nil
	case review.ProposalKindFact:
		reviewParams := ReviewFactProposalParams{ReasonCode: params.ReasonCode, Note: params.Note}
		if params.Confirm {
			value, operationID, replay, err := c.repo.ConfirmFactProposal(ctx, storageIdentity, params.ProposalID, reviewParams)
			if err != nil {
				return review.Proposal{}, "", replay, gatewayError(err)
			}
			return review.NewFactProposal(value), operationID, replay, nil
		}
		value, operationID, replay, err := c.repo.RejectFactProposal(ctx, storageIdentity, params.ProposalID, reviewParams)
		if err != nil {
			return review.Proposal{}, "", replay, gatewayError(err)
		}
		return review.NewFactProposal(value), operationID, replay, nil
	case review.ProposalKindRelation:
		reviewParams := ReviewRelationProposalParams{ReasonCode: params.ReasonCode, Note: params.Note}
		if params.Confirm {
			value, operationID, replay, err := c.repo.ConfirmRelationProposal(ctx, storageIdentity, params.ProposalID, reviewParams)
			if err != nil {
				return review.Proposal{}, "", replay, gatewayError(err)
			}
			return review.NewRelationProposal(value), operationID, replay, nil
		}
		value, operationID, replay, err := c.repo.RejectRelationProposal(ctx, storageIdentity, params.ProposalID, reviewParams)
		if err != nil {
			return review.Proposal{}, "", replay, gatewayError(err)
		}
		return review.NewRelationProposal(value), operationID, replay, nil
	case review.ProposalKindEvolution:
		reviewParams := ReviewEvolutionProposalParams{
			ExpectedTargets: params.ExpectedTargets, ReplacementMode: params.ReplacementMode,
			SupersedeTargets: params.SupersedeTargets, ReasonCode: params.ReasonCode, Note: params.Note,
		}
		if params.Confirm {
			value, operationID, replay, err := c.repo.ConfirmEvolutionProposal(ctx, storageIdentity, params.ProposalID, reviewParams)
			if err != nil {
				return review.Proposal{}, "", replay, gatewayError(err)
			}
			return review.NewEvolutionProposal(value), operationID, replay, nil
		}
		value, operationID, replay, err := c.repo.RejectEvolutionProposal(ctx, storageIdentity, params.ProposalID, reviewParams)
		if err != nil {
			return review.Proposal{}, "", replay, gatewayError(err)
		}
		return review.NewEvolutionProposal(value), operationID, replay, nil
	case review.ProposalKindAggregate:
		reviewParams := ReviewAggregateProposalParams{ReasonCode: params.ReasonCode, Note: params.Note}
		if params.Confirm {
			value, operationID, replay, err := c.repo.ConfirmAggregateProposal(ctx, storageIdentity, params.ProposalID, reviewParams)
			if err != nil {
				return review.Proposal{}, "", replay, gatewayError(err)
			}
			return review.NewAggregateProposal(value), operationID, replay, nil
		}
		value, operationID, replay, err := c.repo.RejectAggregateProposal(ctx, storageIdentity, params.ProposalID, reviewParams)
		if err != nil {
			return review.Proposal{}, "", replay, gatewayError(err)
		}
		return review.NewAggregateProposal(value), operationID, replay, nil
	default:
		return review.Proposal{}, "", false, gateway.NewError(gateway.ErrorInvalidInput, "proposal_type is unsupported")
	}
}
