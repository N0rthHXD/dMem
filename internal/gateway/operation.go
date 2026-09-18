package gateway

import "fmt"

// OperationName is the stable, transport-independent name of a gateway
// operation. It never contains an HTTP method or a route path. Each module
// declares the operation names it publishes as typed constants.
type OperationName string

// Valid reports whether the operation name is non-empty.
func (n OperationName) Valid() bool { return n != "" }

// WriteIdentity is the protocol-neutral identity of a state-changing operation.
// The gateway derives it from the caller identity, the operation name, the
// caller-supplied idempotency key, and the canonical hash of the command.
//
// A protocol adapter must not add HTTP method or route to this identity; those
// are transport concerns and would make replay detection route-dependent.
type WriteIdentity struct {
	Caller         CallerIdentity
	Operation      OperationName
	IdempotencyKey string
	CommandHash    string
	// RequestID is the caller-supplied request identifier carried for audit
	// correlation. It is not part of idempotency semantics and never affects the
	// canonical command hash.
	RequestID string
}

// NewWriteIdentity validates the call context and command and returns the write
// identity used for idempotent persistence. It fails when the caller is absent,
// the idempotency key is absent, the operation name is empty, or the command
// cannot be canonically encoded.
func NewWriteIdentity(call CallContext, operation OperationName, command any) (WriteIdentity, error) {
	if !call.HasCaller() {
		return WriteIdentity{}, ErrMissingCaller
	}
	key := call.IdempotencyKey()
	if key == "" {
		return WriteIdentity{}, ErrMissingIdempotency
	}
	if !operation.Valid() {
		return WriteIdentity{}, fmt.Errorf("gateway: operation name is required")
	}
	hash, err := CanonicalHash(command)
	if err != nil {
		return WriteIdentity{}, err
	}
	return WriteIdentity{
		Caller:         call.Caller,
		Operation:      operation,
		IdempotencyKey: key,
		CommandHash:    hash,
		RequestID:      call.RequestID,
	}, nil
}
