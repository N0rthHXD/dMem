package scope

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// Store is the narrow persistence port the Scope module needs.
type Store interface {
	// CreateScope atomically persists the scope and its initial effective
	// policy. The bool reports an idempotent replay.
	CreateScope(context.Context, gateway.WriteIdentity, CreateParams) (domain.Space, bool, error)
	// GetScope returns one scope by identifier.
	GetScope(context.Context, string) (domain.Space, error)
	// ListScopes returns one page of scopes.
	ListScopes(context.Context, ListParams) (Page, error)
}

// CreateParams is the transport-free persistence input for scope creation.
type CreateParams struct {
	ParentSpaceID *string
	SpaceType     string
	ExternalID    string
	DisplayName   string
}

// ListParams is the transport-free persistence input for scope listing.
type ListParams struct {
	ParentSpaceID *string
	Limit         int
	Cursor        string
}
