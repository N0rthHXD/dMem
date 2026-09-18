package storage

import "github.com/N0rthHXD/dMem/internal/gateway"

// gatewayWriteIdentity maps the protocol-neutral gateway write identity onto the
// storage idempotency record. The stable operation name replaces the legacy
// HTTP method and route so replay detection no longer depends on transport
// details, while the existing idempotency table schema is preserved.
func gatewayWriteIdentity(identity gateway.WriteIdentity) WriteIdentity {
	return WriteIdentity{
		CallerID:       identity.Caller.CallerID(),
		IdempotencyKey: identity.IdempotencyKey,
		RequestID:      identity.RequestID,
		Method:         "",
		Route:          string(identity.Operation),
		RequestHash:    identity.CommandHash,
	}
}
