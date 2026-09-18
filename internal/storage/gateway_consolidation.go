package storage

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/consolidation"
)

// ConsolidationCapability adapts the SQLite repository to the Consolidation
// module's store port. Candidate confirmation remains one atomic use case; the
// adapter never splits classification, authority mutation, review, audit, and
// idempotency across transactions.
type ConsolidationCapability struct {
	repo *Repository
}

// NewConsolidationCapability wires the consolidation store port over one
// repository.
func NewConsolidationCapability(repo *Repository) *ConsolidationCapability {
	return &ConsolidationCapability{repo: repo}
}

// ConfirmCandidate atomically publishes the candidate revision and records its
// review, consolidation decision, audit history, and idempotency result.
func (c *ConsolidationCapability) ConfirmCandidate(ctx context.Context, identity gateway.WriteIdentity, candidateID string, params consolidation.ReviewParams) (domain.Memory, bool, error) {
	memory, replay, err := c.repo.ConfirmCandidate(ctx, gatewayWriteIdentity(identity), candidateID, ReviewCandidateParams{
		ReasonCode: params.ReasonCode,
		Note:       params.Note,
	})
	if err != nil {
		return domain.Memory{}, replay, gatewayError(err)
	}
	return memory, replay, nil
}

// RejectCandidate atomically records the candidate rejection, audit history, and
// idempotency result.
func (c *ConsolidationCapability) RejectCandidate(ctx context.Context, identity gateway.WriteIdentity, candidateID string, params consolidation.ReviewParams) (domain.Candidate, bool, error) {
	candidate, replay, err := c.repo.RejectCandidate(ctx, gatewayWriteIdentity(identity), candidateID, ReviewCandidateParams{
		ReasonCode: params.ReasonCode,
		Note:       params.Note,
	})
	if err != nil {
		return domain.Candidate{}, replay, gatewayError(err)
	}
	return candidate, replay, nil
}

// GetConsolidationDecision returns the recorded decision for a candidate.
func (c *ConsolidationCapability) GetConsolidationDecision(ctx context.Context, candidateID string) (domain.ConsolidationDecision, error) {
	decision, err := c.repo.GetConsolidationDecision(ctx, candidateID)
	if err != nil {
		return domain.ConsolidationDecision{}, gatewayError(err)
	}
	return decision, nil
}
