// Package memory defines the protocol-independent memory authority
// administration surface of dMem: bounded reads over memories, immutable
// revisions, and lifecycle events, plus revision and lifecycle writes. Writes
// keep the revision fence, the append-only audit history, and the
// protocol-neutral idempotent write identity. The module never reaches the
// retrieval pipeline, model providers, or the job runtime.
package memory

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// Operation names published by the memory administration module. Only
// state-changing operations publish stable names; read operations are
// adapter-declared identifiers.
const (
	OperationRevise      gateway.OperationName = "memory.revise"
	OperationStateChange gateway.OperationName = "memory.state_change"
)

// Input bounds enforced by this module before persistence.
const (
	// MaxIdentifierRunes bounds every identifier accepted by the module.
	MaxIdentifierRunes = 128
	// MaxContentRunes bounds one replacement revision body.
	MaxContentRunes = 4096
	// MaxReasonRunes bounds the mandatory administrative reason.
	MaxReasonRunes = 1024
	// MaxEvidence bounds one caller-supplied evidence list.
	MaxEvidence = 64
)

// StateAction is the closed set of lifecycle transitions the administration
// surface may request. It mirrors the storage lifecycle state machine; the
// empty string is not a valid action.
type StateAction string

const (
	// StateConfirm resolves a contested memory back to active.
	StateConfirm StateAction = "confirm"
	// StateContest marks an active memory as contested.
	StateContest StateAction = "contest"
	// StateArchive archives an active or contested memory.
	StateArchive StateAction = "archive"
	// StateRestore restores an archived, expired, retracted, or deleted memory
	// to active using an explicit source revision.
	StateRestore StateAction = "restore"
	// StateRetract retracts an active, contested, or archived memory.
	StateRetract StateAction = "retract"
	// StateFreeze freezes a memory against automatic change.
	StateFreeze StateAction = "freeze"
	// StateUnfreeze clears the frozen flag.
	StateUnfreeze StateAction = "unfreeze"
	// StateDelete logically deletes a memory while preserving all history.
	StateDelete StateAction = "delete"
)

// Valid reports whether the action is one of the closed set.
func (a StateAction) Valid() bool {
	switch a {
	case StateConfirm, StateContest, StateArchive, StateRestore, StateRetract, StateFreeze, StateUnfreeze, StateDelete:
		return true
	default:
		return false
	}
}

// Service is the protocol-facing memory administration surface.
type Service interface {
	List(context.Context, gateway.CallContext, ListQuery) (domain.MemoryPage, error)
	Get(context.Context, gateway.CallContext, GetQuery) (domain.Memory, error)
	ListRevisions(context.Context, gateway.CallContext, MemoryQuery) ([]domain.MemoryRevision, error)
	ListStateEvents(context.Context, gateway.CallContext, MemoryQuery) ([]domain.MemoryStateEvent, error)
	Revise(context.Context, gateway.CallContext, ReviseCommand) (WriteResult, error)
	ChangeState(context.Context, gateway.CallContext, StateChangeCommand) (WriteResult, error)
}

// ListQuery is one bounded memory list read. SpaceID is required because every
// memory belongs to exactly one scope.
type ListQuery struct {
	SpaceID          string
	State            string
	IncludeAncestors bool
	Page             gateway.PageRequest
}

// GetQuery reads one memory by identifier. Administration detail always
// resolves logically deleted memories so the audit history stays reachable.
type GetQuery struct {
	MemoryID string
}

// MemoryQuery reads one memory sub-resource by identifier.
type MemoryQuery struct {
	MemoryID string
}

// Evidence is one caller-supplied evidence reference for a replacement
// revision.
type Evidence struct {
	EventID     string
	Role        domain.EvidenceRole
	StartOffset *int64
	EndOffset   *int64
}

// ReviseCommand replaces the current revision with a new immutable revision.
// A nil Evidence list inherits the current revision evidence; an explicitly
// empty list is rejected because authority requires direct or derived evidence.
type ReviseCommand struct {
	MemoryID           string
	ExpectedRevisionID string
	Content            string
	ValidFrom          *string
	ValidUntil         *string
	Evidence           []Evidence
	Reason             string
}

// StateChangeCommand applies one lifecycle transition under the expected
// current revision.
type StateChangeCommand struct {
	MemoryID           string
	ExpectedRevisionID string
	Action             StateAction
	RestoreRevisionID  *string
	Reason             string
}

// WriteResult is the outcome of one memory administration write.
type WriteResult struct {
	gateway.WriteResult
	Memory domain.Memory
}
