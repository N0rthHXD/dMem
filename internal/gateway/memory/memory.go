// Package memory defines the protocol-independent contracts for direct memory
// authority operations. The service owns semantic validation, protocol-neutral
// defaults, scope checks, idempotency, authority mutation, audit records, and
// the atomic creation of derived-work jobs. It depends only on the narrow ports
// declared in this package.
package memory

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// Operation names published by the Memory module. They are stable identities and
// never include an HTTP method or route.
const (
	OperationRemember gateway.OperationName = "memory.remember"
	OperationForget   gateway.OperationName = "memory.forget"
)

// Service is the inbound, protocol-facing Memory module surface.
type Service interface {
	Remember(context.Context, gateway.CallContext, RememberCommand) (RememberResult, error)
	Get(context.Context, gateway.CallContext, GetQuery) (domain.Memory, error)
	Forget(context.Context, gateway.CallContext, ForgetCommand) (ForgetResult, error)
}

// RememberCommand creates one authoritative memory from supplied content.
type RememberCommand struct {
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

// RememberResult is the outcome of a remember operation.
type RememberResult struct {
	gateway.WriteResult
	Memory  domain.Memory
	EventID string
}

// GetQuery reads one memory by identifier.
type GetQuery struct {
	MemoryID string
}

// ForgetCommand marks one memory as forgotten with an auditable reason.
type ForgetCommand struct {
	MemoryID string
	Reason   string
}

// ForgetResult is the outcome of a forget operation.
type ForgetResult struct {
	gateway.WriteResult
	Memory domain.Memory
}
