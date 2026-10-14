// Package conflict defines the protocol-independent administration surface of
// explicit conflict cases. Resolving a case is a fenced write: every
// participant revision must match the caller expectation, the resolution keeps
// both sides of the history, and reopening a closed case is impossible. The
// module never reaches the retrieval pipeline, model providers, or the job
// runtime.
package conflict

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// OperationResolve is the stable operation name of the conflict resolution
// write. Reads are adapter-declared identifiers.
const OperationResolve gateway.OperationName = "conflict.resolve"

// Input bounds enforced by this module before persistence.
const (
	// MaxIdentifierRunes bounds every identifier accepted by the module.
	MaxIdentifierRunes = 128
	// MaxReasonRunes bounds the mandatory resolution reason.
	MaxReasonRunes = 1024
	// MaxTargets bounds the expected participant list.
	MaxTargets = 16
	// MaxEvidence bounds one resolution evidence list.
	MaxEvidence = 64
	// MaxContentRunes bounds one replacement or reconciled revision body.
	MaxContentRunes = 4096
)

// ResolutionType is the closed set of conflict resolutions.
type ResolutionType string

const (
	// ResolutionKeepBoth leaves both sides active and closes the case.
	ResolutionKeepBoth ResolutionType = "keep_both"
	// ResolutionDismiss dismisses the conflict assertions and closes the case.
	ResolutionDismiss ResolutionType = "dismiss"
	// ResolutionReviseOne replaces one participant revision with new content.
	ResolutionReviseOne ResolutionType = "revise_one"
	// ResolutionSupersede supersedes one participant revision.
	ResolutionSupersede ResolutionType = "supersede"
	// ResolutionReconcile creates one new revision reconciling all participants.
	ResolutionReconcile ResolutionType = "reconcile"
)

// Valid reports whether the resolution type is one of the closed set.
func (r ResolutionType) Valid() bool {
	switch r {
	case ResolutionKeepBoth, ResolutionDismiss, ResolutionReviseOne, ResolutionSupersede, ResolutionReconcile:
		return true
	default:
		return false
	}
}

// Destructive reports whether the resolution publishes new authority. Storage
// requires proposed content and evidence for every destructive resolution and
// rejects them for non-destructive ones.
func (r ResolutionType) Destructive() bool {
	switch r {
	case ResolutionReviseOne, ResolutionSupersede, ResolutionReconcile:
		return true
	default:
		return false
	}
}

// Service is the protocol-facing conflict administration surface.
type Service interface {
	List(context.Context, gateway.CallContext, ListQuery) (domain.ConflictCasePage, error)
	Get(context.Context, gateway.CallContext, GetQuery) (domain.ConflictCase, error)
	Resolve(context.Context, gateway.CallContext, ResolveCommand) (ResolveResult, error)
}

// ListQuery is one bounded conflict case list read. SpaceID is required because
// every case belongs to exactly one scope.
type ListQuery struct {
	SpaceID string
	State   string
	Page    gateway.PageRequest
}

// GetQuery reads one conflict case by identifier.
type GetQuery struct {
	CaseID string
}

// Evidence is one caller-supplied evidence reference for a resolution revision.
type Evidence struct {
	EventID     string
	Role        domain.EvidenceRole
	StartOffset *int64
	EndOffset   *int64
}

// ResolveCommand closes one open conflict case. ExpectedTargets must match the
// participant revisions exactly; the storage layer revalidates the fence.
type ResolveCommand struct {
	CaseID          string
	ResolutionType  ResolutionType
	ExpectedTargets []domain.EvolutionTarget
	SelectedTarget  *domain.EvolutionTarget
	ProposedContent *string
	ValidFrom       *string
	ValidUntil      *string
	Evidence        []Evidence
	ReplacementMode *string
	ResultStates    map[string]string
	Reason          string
}

// ResolveResult is the terminal state of one resolved conflict case.
type ResolveResult struct {
	gateway.WriteResult
	Case domain.ConflictCase
}
