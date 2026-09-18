package consolidation

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// Store is the narrow persistence port the Consolidation module needs. Review
// and authority mutation stay in one atomic operation per candidate.
type Store interface {
	// ConfirmCandidate atomically publishes the candidate revision and records
	// its review and consolidation decision. The bool reports an idempotent
	// replay.
	ConfirmCandidate(context.Context, gateway.WriteIdentity, string, ReviewParams) (domain.Memory, bool, error)
	// RejectCandidate atomically records the candidate rejection. The bool
	// reports an idempotent replay.
	RejectCandidate(context.Context, gateway.WriteIdentity, string, ReviewParams) (domain.Candidate, bool, error)
	// GetConsolidationDecision returns the recorded decision for a candidate.
	GetConsolidationDecision(context.Context, string) (domain.ConsolidationDecision, error)
}

// ReviewParams is the transport-free review input.
type ReviewParams struct {
	ReasonCode string
	Note       *string
}
