package memory

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// Store is the narrow persistence port the memory administration module needs.
// It exposes bounded reads and the two fenced administrative writes; it never
// exposes direct authority insertion or bypasses the lifecycle state machine.
type Store interface {
	// ListMemories returns one bounded page of memories for one scope.
	ListMemories(context.Context, ListFilter) (domain.MemoryPage, error)
	// GetMemory returns one memory by identifier. includeDeleted controls
	// whether logically deleted memories are resolved.
	GetMemory(context.Context, string, bool) (domain.Memory, error)
	// ListMemoryRevisions returns the immutable revision history of one memory.
	ListMemoryRevisions(context.Context, string) ([]domain.MemoryRevision, error)
	// ListMemoryStateEvents returns the append-only lifecycle events of one memory.
	ListMemoryStateEvents(context.Context, string) ([]domain.MemoryStateEvent, error)
	// ReviseMemory appends one immutable revision under the expected-revision
	// fence. The string is the persisted operation identifier and the bool
	// reports an idempotent replay.
	ReviseMemory(context.Context, gateway.WriteIdentity, ReviseParams) (domain.Memory, string, bool, error)
	// ChangeMemoryState applies one lifecycle transition under the
	// expected-revision fence. The string is the persisted operation identifier
	// and the bool reports an idempotent replay.
	ChangeMemoryState(context.Context, gateway.WriteIdentity, StateChangeParams) (domain.Memory, string, bool, error)
}

// ListFilter is the transport-free memory list filter.
type ListFilter struct {
	SpaceID          string
	State            string
	IncludeAncestors bool
	Limit            int
	Cursor           string
}

// EvidenceParams is the transport-free evidence reference.
type EvidenceParams struct {
	EventID     string
	Role        domain.EvidenceRole
	StartOffset *int64
	EndOffset   *int64
}

// ReviseParams is the transport-free persistence input for one revision write.
type ReviseParams struct {
	MemoryID           string
	ExpectedRevisionID string
	Content            string
	ValidFrom          *string
	ValidUntil         *string
	Evidence           []EvidenceParams
	Reason             string
}

// StateChangeParams is the transport-free persistence input for one lifecycle
// transition.
type StateChangeParams struct {
	MemoryID           string
	ExpectedRevisionID string
	Action             string
	RestoreRevisionID  *string
	Reason             string
}
