package storage

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/memory"
)

// MemoryCapability adapts the SQLite memory authority routines to the memory
// administration module's store port. It translates storage failures into
// stable gateway error codes while retaining the original cause.
type MemoryCapability struct {
	repo *Repository
}

// NewMemoryCapability wires the memory administration store port over one
// repository.
func NewMemoryCapability(repo *Repository) *MemoryCapability {
	return &MemoryCapability{repo: repo}
}

// ListMemories returns one bounded page of memories for a scope.
func (c *MemoryCapability) ListMemories(ctx context.Context, filter memory.ListFilter) (domain.MemoryPage, error) {
	page, err := c.repo.ListMemories(ctx, MemoryFilter{
		SpaceID:          filter.SpaceID,
		IncludeAncestors: filter.IncludeAncestors,
		State:            filter.State,
		Limit:            filter.Limit,
		Cursor:           filter.Cursor,
	})
	if err != nil {
		return domain.MemoryPage{}, gatewayError(err)
	}
	return page, nil
}

// GetMemory returns one memory by identifier. Logically deleted memories are
// resolved when includeDeleted is set so the administration surface keeps the
// audit history reachable.
func (c *MemoryCapability) GetMemory(ctx context.Context, memoryID string, includeDeleted bool) (domain.Memory, error) {
	value, err := getMemory(ctx, c.repo.store.reader, memoryID, !includeDeleted)
	if err != nil {
		return domain.Memory{}, gatewayError(err)
	}
	return value, nil
}

// ListMemoryRevisions returns the immutable revision history of one memory.
func (c *MemoryCapability) ListMemoryRevisions(ctx context.Context, memoryID string) ([]domain.MemoryRevision, error) {
	revisions, err := c.repo.ListMemoryRevisions(ctx, memoryID)
	if err != nil {
		return nil, gatewayError(err)
	}
	return revisions, nil
}

// ListMemoryStateEvents returns the append-only lifecycle events of one memory.
func (c *MemoryCapability) ListMemoryStateEvents(ctx context.Context, memoryID string) ([]domain.MemoryStateEvent, error) {
	events, err := c.repo.ListMemoryStateEvents(ctx, memoryID)
	if err != nil {
		return nil, gatewayError(err)
	}
	return events, nil
}

// ReviseMemory appends one immutable revision under the expected-revision
// fence.
func (c *MemoryCapability) ReviseMemory(ctx context.Context, identity gateway.WriteIdentity, params memory.ReviseParams) (domain.Memory, string, bool, error) {
	stored, operationID, replay, err := c.repo.ReviseMemory(ctx, gatewayWriteIdentity(identity), params.MemoryID, ReviseMemoryParams{
		ExpectedRevisionID: params.ExpectedRevisionID,
		Content:            params.Content,
		ValidFrom:          params.ValidFrom,
		ValidUntil:         params.ValidUntil,
		Evidence:           evidenceParamsFromGateway(params.Evidence),
		Reason:             params.Reason,
	})
	if err != nil {
		return domain.Memory{}, "", replay, gatewayError(err)
	}
	return stored, operationID, replay, nil
}

// ChangeMemoryState applies one lifecycle transition under the
// expected-revision fence.
func (c *MemoryCapability) ChangeMemoryState(ctx context.Context, identity gateway.WriteIdentity, params memory.StateChangeParams) (domain.Memory, string, bool, error) {
	stored, operationID, replay, err := c.repo.ChangeMemoryState(ctx, gatewayWriteIdentity(identity), params.MemoryID, ChangeMemoryStateParams{
		ExpectedRevisionID: params.ExpectedRevisionID,
		Action:             params.Action,
		RestoreRevisionID:  params.RestoreRevisionID,
		Reason:             params.Reason,
	})
	if err != nil {
		return domain.Memory{}, "", replay, gatewayError(err)
	}
	return stored, operationID, replay, nil
}

func evidenceParamsFromGateway(items []memory.EvidenceParams) []EvidenceParams {
	result := make([]EvidenceParams, len(items))
	for i, item := range items {
		result[i] = EvidenceParams{EventID: item.EventID, Role: item.Role, StartOffset: item.StartOffset, EndOffset: item.EndOffset}
	}
	return result
}
