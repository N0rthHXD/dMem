package scope

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// Store is the narrow atomic persistence capability required by Scope.
type Store interface {
	EnsureScope(context.Context, gateway.WriteIdentity, EnsureParams) (domain.Space, bool, error)
	GetScope(context.Context, string) (domain.Space, error)
}

type EnsureParams struct {
	ParentSpaceID *string
	SpaceType     string
	ExternalID    string
	DisplayName   string
}

// The repository's administrative adapter still uses these transport-free
// values internally; they are intentionally absent from the inbound Service.
type CreateParams struct {
	ParentSpaceID *string
	SpaceType     string
	ExternalID    string
	DisplayName   string
}

type ResolveParams struct {
	ParentSpaceID *string
	SpaceType     string
	ExternalID    string
}

type ListParams struct {
	ParentSpaceID *string
	Limit         int
	Cursor        string
}
