package conflict

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// Store is the narrow persistence port the conflict module needs. It exposes
// the bounded case reads and the single fenced resolution write.
type Store interface {
	// ListConflictCases returns one bounded page of conflict cases.
	ListConflictCases(context.Context, ConflictFilter) (domain.ConflictCasePage, error)
	// GetConflictCase returns one conflict case by identifier.
	GetConflictCase(context.Context, string) (domain.ConflictCase, error)
	// ResolveConflict closes one open case inside one transaction. The string
	// is the persisted operation identifier and the bool reports an idempotent
	// replay.
	ResolveConflict(context.Context, gateway.WriteIdentity, ResolveParams) (domain.ConflictCase, string, bool, error)
}

// ConflictFilter is the transport-free conflict case list filter.
type ConflictFilter struct {
	SpaceID string
	State   string
	Limit   int
	Cursor  string
}

// ResolveParams is the transport-free persistence input for one resolution.
type ResolveParams struct {
	CaseID          string
	ResolutionType  string
	ExpectedTargets []domain.EvolutionTarget
	SelectedTarget  *domain.EvolutionTarget
	ProposedContent *string
	ValidFrom       *string
	ValidUntil      *string
	Evidence        []EvidenceParams
	ReplacementMode *string
	ResultStates    map[string]string
	Reason          string
}

// EvidenceParams is the transport-free evidence reference.
type EvidenceParams struct {
	EventID     string
	Role        domain.EvidenceRole
	StartOffset *int64
	EndOffset   *int64
}
