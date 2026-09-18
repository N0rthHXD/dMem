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
	return c.repo.ConfirmCandidate(ctx, gatewayWriteIdentity(identity), candidateID, ReviewCandidateParams{
		ReasonCode: params.ReasonCode,
		Note:       params.Note,
	})
}

// RejectCandidate atomically records the candidate rejection, audit history, and
// idempotency result.
func (c *ConsolidationCapability) RejectCandidate(ctx context.Context, identity gateway.WriteIdentity, candidateID string, params consolidation.ReviewParams) (domain.Candidate, bool, error) {
	return c.repo.RejectCandidate(ctx, gatewayWriteIdentity(identity), candidateID, ReviewCandidateParams{
		ReasonCode: params.ReasonCode,
		Note:       params.Note,
	})
}

// GetConsolidationDecision returns the recorded decision for a candidate.
func (c *ConsolidationCapability) GetConsolidationDecision(ctx context.Context, candidateID string) (domain.ConsolidationDecision, error) {
	return c.repo.GetConsolidationDecision(ctx, candidateID)
}

// ConsolidationClassifierBridge adapts a protocol-neutral, provider-backed
// classifier to the repository's internal consolidation classifier hook. It
// translates the classifier data shape without moving authority decisions out of
// the deterministic confirmation path.
type ConsolidationClassifierBridge struct {
	Classifier consolidation.Classifier
}

// ClassifyConsolidation maps the bounded neighbor set and delegates one provider
// classification.
func (b *ConsolidationClassifierBridge) ClassifyConsolidation(ctx context.Context, candidate domain.MemoryRevision, neighbors []ConsolidationNeighbor) (ConsolidationProviderProposal, error) {
	bounded := make([]consolidation.Neighbor, 0, len(neighbors))
	for _, neighbor := range neighbors {
		bounded = append(bounded, consolidation.Neighbor{
			MemoryID:         neighbor.MemoryID,
			RevisionID:       neighbor.RevisionID,
			Kind:             neighbor.Kind,
			CanonicalHash:    neighbor.CanonicalHash,
			CanonicalContent: neighbor.CanonicalContent,
			Subjects:         neighbor.Subjects,
			Objects:          neighbor.Objects,
			Attributes:       neighbor.Attributes,
			EvidenceEventIDs: neighbor.EvidenceEventIDs,
			Similarity:       neighbor.Similarity,
		})
	}
	classification, err := b.Classifier.Classify(ctx, consolidation.ClassifyRequest{Candidate: candidate, Neighbors: bounded})
	if err != nil {
		return ConsolidationProviderProposal{}, err
	}
	return ConsolidationProviderProposal{
		Action:     classification.Action,
		ReasonCode: classification.ReasonCode,
		MemoryID:   classification.MemoryID,
		RevisionID: classification.RevisionID,
		Similarity: classification.Similarity,
	}, nil
}
