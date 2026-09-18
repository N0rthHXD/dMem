package memory

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// AuthorityStore is the narrow persistence port the Memory module needs. A
// runtime adapter implements it without exposing storage DTOs to the module.
type AuthorityStore interface {
	// Remember atomically persists the memory, its audit record, and any
	// required derived-work job. The bool reports an idempotent replay.
	Remember(context.Context, gateway.WriteIdentity, RememberParams) (RememberRecord, bool, error)
	// GetMemory returns the current memory projection.
	GetMemory(context.Context, string) (domain.Memory, error)
	// ForgetMemory atomically applies the deletion state and audit record. The
	// bool reports an idempotent replay.
	ForgetMemory(context.Context, gateway.WriteIdentity, string, string) (domain.Memory, bool, error)
}

// RememberParams is the transport-free persistence input for a remember write.
type RememberParams struct {
	SpaceID         string
	Content         string
	Kind            string
	Sensitivity     string
	ValidFrom       *string
	ValidUntil      *string
	SourceName      string
	SourceEventID   *string
	SourceStreamKey string
	Sequence        *int64
	OccurredAt      string
	Metadata        map[string]any
	Reason          string
}

// RememberRecord is the persisted result of a remember write.
type RememberRecord struct {
	Memory  domain.Memory
	EventID string
}
