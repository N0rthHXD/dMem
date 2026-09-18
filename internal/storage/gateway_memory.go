package storage

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/memory"
)

// MemoryCapability adapts the SQLite repository to the Memory module's
// authority port. It maps protocol-neutral commands onto the existing atomic
// repository routines and never exposes storage DTOs to the gateway.
type MemoryCapability struct {
	repo *Repository
}

// NewMemoryCapability wires the Memory authority port over one repository.
func NewMemoryCapability(repo *Repository) *MemoryCapability {
	return &MemoryCapability{repo: repo}
}

// Remember atomically persists the memory, event, evidence, audit record, and
// idempotency result.
func (c *MemoryCapability) Remember(ctx context.Context, identity gateway.WriteIdentity, params memory.RememberParams) (memory.RememberRecord, bool, error) {
	result, replay, err := c.repo.Remember(ctx, gatewayWriteIdentity(identity), RememberParams{
		SpaceID:         params.SpaceID,
		Content:         params.Content,
		Kind:            params.Kind,
		Sensitivity:     params.Sensitivity,
		ValidFrom:       params.ValidFrom,
		ValidUntil:      params.ValidUntil,
		SourceName:      params.SourceName,
		SourceEventID:   params.SourceEventID,
		SourceStreamKey: params.SourceStreamKey,
		Sequence:        params.Sequence,
		OccurredAt:      params.OccurredAt,
		Metadata:        params.Metadata,
		Reason:          params.Reason,
	})
	if err != nil {
		return memory.RememberRecord{}, replay, err
	}
	return memory.RememberRecord{Memory: result.Memory, EventID: result.EventID}, replay, nil
}

// GetMemory returns the current memory projection.
func (c *MemoryCapability) GetMemory(ctx context.Context, memoryID string) (domain.Memory, error) {
	return c.repo.GetMemory(ctx, memoryID)
}

// ForgetMemory atomically applies the deletion state, audit record, and
// idempotency result.
func (c *MemoryCapability) ForgetMemory(ctx context.Context, identity gateway.WriteIdentity, memoryID, reason string) (domain.Memory, bool, error) {
	return c.repo.ForgetMemory(ctx, gatewayWriteIdentity(identity), memoryID, reason)
}
