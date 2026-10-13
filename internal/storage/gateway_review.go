package storage

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/review"
)

// ReviewCapability adapts the SQLite candidate review routines to the Review
// module's candidate store port. It translates storage failures into stable
// gateway error codes while retaining the original cause.
type ReviewCapability struct {
	repo *Repository
}

// NewReviewCapability wires the candidate review port over one repository.
func NewReviewCapability(repo *Repository) *ReviewCapability {
	return &ReviewCapability{repo: repo}
}

// ListCandidates returns one bounded page of candidates for a space.
func (c *ReviewCapability) ListCandidates(ctx context.Context, filter review.CandidateFilter) (domain.CandidatePage, error) {
	page, err := c.repo.ListCandidates(ctx, CandidateFilter{
		SpaceID:           filter.SpaceID,
		ReviewState:       filter.ReviewState,
		MemoryType:        filter.MemoryType,
		ProcessorIdentity: filter.ProcessorIdentity,
		ProcessingVersion: filter.ProcessingVersion,
		CreatedFrom:       filter.CreatedFrom,
		CreatedUntil:      filter.CreatedUntil,
		Limit:             filter.Limit,
		Cursor:            filter.Cursor,
	})
	if err != nil {
		return domain.CandidatePage{}, gatewayError(err)
	}
	return page, nil
}

// GetCandidate returns one candidate by identifier.
func (c *ReviewCapability) GetCandidate(ctx context.Context, candidateID string) (domain.Candidate, error) {
	candidate, err := c.repo.GetCandidate(ctx, candidateID)
	if err != nil {
		return domain.Candidate{}, gatewayError(err)
	}
	return candidate, nil
}

// ConfirmCandidate confirms one pending candidate.
func (c *ReviewCapability) ConfirmCandidate(ctx context.Context, identity gateway.WriteIdentity, params review.ReviewParams) (domain.Memory, string, bool, error) {
	memory, operationID, replay, err := c.repo.ConfirmCandidate(ctx, gatewayWriteIdentity(identity), params.CandidateID, ReviewCandidateParams{ReasonCode: params.ReasonCode, Note: params.Note})
	if err != nil {
		return domain.Memory{}, "", replay, gatewayError(err)
	}
	return memory, operationID, replay, nil
}

// RejectCandidate rejects one pending candidate.
func (c *ReviewCapability) RejectCandidate(ctx context.Context, identity gateway.WriteIdentity, params review.ReviewParams) (domain.Candidate, string, bool, error) {
	candidate, operationID, replay, err := c.repo.RejectCandidate(ctx, gatewayWriteIdentity(identity), params.CandidateID, ReviewCandidateParams{ReasonCode: params.ReasonCode, Note: params.Note})
	if err != nil {
		return domain.Candidate{}, "", replay, gatewayError(err)
	}
	return candidate, operationID, replay, nil
}
