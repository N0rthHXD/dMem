package storage

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/scope"
)

// ScopeCapability adapts the SQLite repository to the Scope module's store port.
type ScopeCapability struct {
	repo *Repository
}

// NewScopeCapability wires the Scope store port over one repository.
func NewScopeCapability(repo *Repository) *ScopeCapability {
	return &ScopeCapability{repo: repo}
}

// CreateScope atomically persists the scope, its initial effective policy,
// audit record, and idempotency result.
func (c *ScopeCapability) CreateScope(ctx context.Context, identity gateway.WriteIdentity, params scope.CreateParams) (domain.Space, bool, error) {
	return c.repo.CreateSpace(ctx, gatewayWriteIdentity(identity), CreateSpaceParams{
		ParentSpaceID: params.ParentSpaceID,
		SpaceType:     params.SpaceType,
		ExternalID:    params.ExternalID,
		DisplayName:   params.DisplayName,
	})
}

// GetScope returns one scope by identifier.
func (c *ScopeCapability) GetScope(ctx context.Context, spaceID string) (domain.Space, error) {
	return c.repo.GetSpace(ctx, spaceID)
}

// ListScopes returns one cursor page of scopes.
func (c *ScopeCapability) ListScopes(ctx context.Context, params scope.ListParams) (scope.Page, error) {
	page, err := c.repo.ListSpaces(ctx, params.ParentSpaceID, params.Limit, params.Cursor)
	if err != nil {
		return scope.Page{}, err
	}
	return scope.Page{Items: page.Items, NextCursor: page.NextCursor}, nil
}
