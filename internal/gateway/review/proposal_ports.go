package review

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// ProposalStore is the narrow persistence port the proposal review service
// needs. It never exposes proposal mutation outside the review state machine.
type ProposalStore interface {
	// ListProposals returns one bounded page of proposals of exactly one family.
	ListProposals(context.Context, ProposalFilter) (ProposalPage, error)
	// GetProposal returns one proposal by family and identifier.
	GetProposal(context.Context, ProposalKind, string) (Proposal, error)
	// ReviewProposal confirms or rejects one pending proposal inside one
	// transaction. The string is the persisted operation identifier and the
	// bool reports an idempotent replay.
	ReviewProposal(context.Context, gateway.WriteIdentity, ReviewProposalParams) (Proposal, string, bool, error)
}

// ProposalFilter is the transport-free proposal list filter. Kind is required.
type ProposalFilter struct {
	Kind        ProposalKind
	SpaceID     string
	ReviewState string
	Limit       int
	Cursor      string
}

// ReviewProposalParams is the transport-free persistence input for one review.
type ReviewProposalParams struct {
	Kind             ProposalKind
	ProposalID       string
	Confirm          bool
	ReasonCode       string
	Note             *string
	ExpectedTargets  []domain.EvolutionTarget
	ReplacementMode  *string
	SupersedeTargets bool
}
