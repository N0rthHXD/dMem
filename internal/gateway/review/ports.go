package review

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// CandidateStore is the narrow persistence port the candidate review module
// needs. It never exposes candidate mutation outside the review state machine.
type CandidateStore interface {
	// ListCandidates returns one bounded page of candidates.
	ListCandidates(context.Context, CandidateFilter) (domain.CandidatePage, error)
	// GetCandidate returns one candidate by identifier.
	GetCandidate(context.Context, string) (domain.Candidate, error)
	// ConfirmCandidate publishes the candidate revision or attaches it to an
	// existing revision inside one transaction. The string is the persisted
	// operation identifier and the bool reports an idempotent replay.
	ConfirmCandidate(context.Context, gateway.WriteIdentity, ReviewParams) (domain.Memory, string, bool, error)
	// RejectCandidate discards a pending candidate inside one transaction. The
	// string is the persisted operation identifier and the bool reports an
	// idempotent replay.
	RejectCandidate(context.Context, gateway.WriteIdentity, ReviewParams) (domain.Candidate, string, bool, error)
}

// CandidateFilter is the transport-free candidate list filter.
type CandidateFilter struct {
	SpaceID           string
	ReviewState       string
	MemoryType        string
	ProcessorIdentity string
	ProcessingVersion string
	CreatedFrom       string
	CreatedUntil      string
	Limit             int
	Cursor            string
}

// ReviewParams is the transport-free persistence input for one review.
type ReviewParams struct {
	CandidateID string
	ReasonCode  string
	Note        *string
}
