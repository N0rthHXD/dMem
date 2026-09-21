package storage

import (
	"context"
	"database/sql"
	"errors"

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

// EnsureScope implements the current scope port while preserving CreateScope
// for administrative callers that still use the explicit create operation.
func (c *ScopeCapability) EnsureScope(ctx context.Context, identity gateway.WriteIdentity, params scope.EnsureParams) (domain.Space, bool, error) {
	resolved, err := c.repo.GetSpaceByExternalIdentity(ctx, params.ParentSpaceID, params.SpaceType, params.ExternalID)
	if err == nil {
		return resolved, true, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return domain.Space{}, false, gatewayError(err)
	}
	return c.CreateScope(ctx, identity, scope.CreateParams(params))
}

// CreateScope atomically persists the scope, its initial effective policy,
// audit record, and idempotency result.
func (c *ScopeCapability) CreateScope(ctx context.Context, identity gateway.WriteIdentity, params scope.CreateParams) (domain.Space, bool, error) {
	created, replay, err := c.repo.CreateSpace(ctx, gatewayWriteIdentity(identity), CreateSpaceParams{
		ParentSpaceID: params.ParentSpaceID,
		SpaceType:     params.SpaceType,
		ExternalID:    params.ExternalID,
		DisplayName:   params.DisplayName,
	})
	if err != nil {
		return domain.Space{}, replay, gatewayError(err)
	}
	return created, replay, nil
}

// ResolveScope returns the scope matching a stable external identity.
func (c *ScopeCapability) ResolveScope(ctx context.Context, params scope.ResolveParams) (domain.Space, error) {
	resolved, err := c.repo.GetSpaceByExternalIdentity(ctx, params.ParentSpaceID, params.SpaceType, params.ExternalID)
	if err != nil {
		return domain.Space{}, gatewayError(err)
	}
	return resolved, nil
}

// GetScope returns one scope by identifier.
func (c *ScopeCapability) GetScope(ctx context.Context, spaceID string) (domain.Space, error) {
	found, err := c.repo.GetSpace(ctx, spaceID)
	if err != nil {
		return domain.Space{}, gatewayError(err)
	}
	return found, nil
}

// ListScopes returns one cursor page of scopes.
func (c *ScopeCapability) ListScopes(ctx context.Context, params scope.ListParams) (scope.Page, error) {
	page, err := c.repo.ListSpaces(ctx, params.ParentSpaceID, params.Limit, params.Cursor)
	if err != nil {
		return scope.Page{}, gatewayError(err)
	}
	return scope.Page{Items: page.Items, NextCursor: page.NextCursor}, nil
}

// GetSpaceByExternalIdentity returns the scope matching the parent, type, and
// external identifier that form its stable external identity.
func (r *Repository) GetSpaceByExternalIdentity(ctx context.Context, parentID *string, spaceType, externalID string) (domain.Space, error) {
	query := `SELECT space_id, parent_space_id, space_type, external_id, display_name, created_at FROM memory_spaces WHERE space_type = ? AND external_id = ?`
	args := []any{spaceType, externalID}
	if parentID == nil {
		query += ` AND parent_space_id IS NULL`
	} else {
		query += ` AND parent_space_id = ?`
		args = append(args, *parentID)
	}
	query += ` ORDER BY space_id ASC LIMIT 1`
	var result domain.Space
	err := r.store.reader.QueryRowContext(ctx, query, args...).Scan(
		&result.SpaceID, &result.ParentSpaceID, &result.SpaceType, &result.ExternalID, &result.DisplayName, &result.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Space{}, ErrNotFound
	}
	return result, err
}
