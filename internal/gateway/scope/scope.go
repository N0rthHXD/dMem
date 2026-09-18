// Package scope defines the protocol-independent contracts for the scope
// hierarchy and its stable external identity. Internal storage may continue to
// use the term "space"; the gateway contract uses "scope" consistently.
package scope

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// OperationCreate is the stable operation name for scope creation.
const OperationCreate gateway.OperationName = "scope.create"

// Service is the inbound, protocol-facing Scope module surface.
type Service interface {
	Create(context.Context, gateway.CallContext, CreateCommand) (CreateResult, error)
	Resolve(context.Context, gateway.CallContext, ResolveQuery) (domain.Space, error)
	Get(context.Context, gateway.CallContext, GetQuery) (domain.Space, error)
	List(context.Context, gateway.CallContext, ListQuery) (Page, error)
}

// CreateCommand creates one scope in the hierarchy.
type CreateCommand struct {
	ParentSpaceID *string
	SpaceType     string
	ExternalID    string
	DisplayName   string
}

// CreateResult is the outcome of a scope creation.
type CreateResult struct {
	gateway.WriteResult
	Scope domain.Space
}

// ResolveQuery looks up one scope by its stable external identity: the parent,
// the scope type, and the caller-supplied external identifier.
type ResolveQuery struct {
	ParentSpaceID *string
	SpaceType     string
	ExternalID    string
}

// GetQuery reads one scope by identifier.
type GetQuery struct {
	SpaceID string
}

// ListQuery lists scopes, optionally filtered by parent.
type ListQuery struct {
	ParentSpaceID *string
	Page          gateway.PageRequest
}

// Page is one page of scopes.
type Page struct {
	Items      []domain.Space
	NextCursor *string
}
