// Package scope defines the protocol-independent contracts for the scope
// hierarchy and its stable external identity. Internal storage may continue to
// use the term "space"; the gateway contract uses "scope" consistently.
package scope

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// OperationEnsure atomically resolves or creates a stable external scope.
const OperationEnsure gateway.OperationName = "scope.ensure"

// Service is the inbound, protocol-facing Scope module surface.
type Service interface {
	Get(context.Context, gateway.CallContext, GetQuery) (domain.Space, error)
	Ensure(context.Context, gateway.CallContext, EnsureCommand) (EnsureResult, error)
}

type EnsureCommand struct {
	ParentSpaceID *string
	SpaceType     string
	ExternalID    string
	DisplayName   string
}

type EnsureResult struct {
	gateway.WriteResult
	Scope domain.Space
}

// GetQuery reads one scope by identifier.
type GetQuery struct {
	SpaceID string
}

type Page struct {
	Items      []domain.Space
	NextCursor *string
}
